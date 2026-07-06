// Package playback ports plexctl/playback.py: direct-to-client Companion
// commands against the Apple TV's own baseurl (not PMS) — play/pause/stop/
// step/volume, the seek pause dance, and playMedia/playQueue.
//
// Direct-to-client HTTP is required for the Apple TV: proxying through PMS
// with X-Plex-Target-Client-Identifier silently times out. commandID must be
// monotonically increasing across CLI invocations — the Apple TV drops
// anything at or below the last ID it processed — so it is drawn from a
// flock-protected counter file in config.Dir(), guaranteeing strictly-
// increasing IDs across concurrent processes. (The old per-process
// time.Now().Unix() seed let two invocations in the same wall-clock second
// issue the same ID, and the Apple TV silently dropped the second command.)
package playback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/config"
	"github.com/corinthian/plexctl/internal/jsonx"
)

// CompanionTransportError mirrors playback.CompanionTransportError.
type CompanionTransportError struct{ Msg string }

func (e *CompanionTransportError) Error() string { return e.Msg }

// --- commandID -----------------------------------------------------------

// nowUnix is a seam so tests can freeze the clock and prove the persisted
// counter alone keeps IDs increasing.
var nowUnix = func() int64 { return time.Now().Unix() }

// commandID / commandIDSeeded back the in-memory fallback used only when the
// counter file is unreachable or corrupt. On the happy path the file is the
// source of truth and commandID just shadows the last issued value so a
// mid-run file failure resumes monotonically instead of reseeding.
var (
	commandIDMu     sync.Mutex
	commandID       int64
	commandIDSeeded bool
)

// commandIDPath is the flock-protected counter file, riding
// $PLEXCTL_CONFIG_DIR the same way queue state does.
func commandIDPath() string {
	return filepath.Join(config.Dir(), "commandid")
}

// nextPersistedCommandID computes and persists the next strictly-increasing
// commandID under an exclusive file lock, so IDs never collide across
// concurrent CLI processes. Returns ok=false on any filesystem/lock failure
// or a corrupt counter file, signalling the caller to fall back to the
// in-memory epoch seed.
func nextPersistedCommandID() (int64, bool) {
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		return 0, false
	}
	f, err := os.OpenFile(commandIDPath(), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 0, false
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	next := nowUnix()
	b, err := io.ReadAll(f)
	if err != nil {
		return 0, false
	}
	if s := strings.TrimSpace(string(b)); s != "" {
		persisted, perr := strconv.ParseInt(s, 10, 64)
		if perr != nil {
			// Corrupt counter file: fall back to the in-memory seed rather
			// than trusting or clobbering it.
			return 0, false
		}
		if persisted+1 > next {
			next = persisted + 1
		}
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, false
	}
	if err := f.Truncate(0); err != nil {
		return 0, false
	}
	if _, err := f.WriteString(strconv.FormatInt(next, 10)); err != nil {
		return 0, false
	}
	return next, true
}

func nextCommandID() int64 {
	commandIDMu.Lock()
	defer commandIDMu.Unlock()
	if id, ok := nextPersistedCommandID(); ok {
		// Shadow the persisted value so a later file failure continues from
		// here instead of reseeding from the clock.
		commandID = id
		commandIDSeeded = true
		return id
	}
	// Fallback: counter file unreachable/corrupt — pure in-memory epoch seed
	// (the pre-B5 behavior).
	if !commandIDSeeded {
		commandID = nowUnix()
		commandIDSeeded = true
	}
	commandID++
	return commandID
}

// --- shared Companion transport -------------------------------------------

// companionHeaders builds the header set shared by _player_cmd and
// _player_get in the Python original.
func companionHeaders(client jsonx.J) map[string]string {
	cfg := config.Load()
	token := config.Require("token")
	clientID := config.StringOr(cfg, "client_id", config.Defaults["client_id"])
	headers := api.Headers(token, clientID)
	headers["X-Plex-Target-Client-Identifier"] = jsonx.AsStr(client["machineIdentifier"])
	return headers
}

// companionGet performs the GET against the client's Companion endpoint and
// returns the raw (unclassified) transport error, if any, so callers can
// apply their own error-shape conventions.
func companionGet(client jsonx.J, path string, params url.Values) (*http.Response, []byte, error) {
	headers := companionHeaders(client)
	base := strings.TrimRight(jsonx.AsStr(client["baseurl"]), "/")
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		return nil, nil, err
	}
	req.URL.RawQuery = params.Encode()
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	httpClient := &http.Client{Timeout: time.Duration(api.DefaultTimeout() * float64(time.Second))}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, nil, err
	}
	return resp, body, nil
}

// classifyTransportErr mirrors _player_cmd's exception ladder: a connect
// timeout must classify as a timeout before the generic connection-failed
// branch (ConnectTimeout subclasses both in requests).
func classifyTransportErr(err error) jsonx.J {
	var ne net.Error
	if (errors.As(err, &ne) && ne.Timeout()) || errors.Is(err, context.DeadlineExceeded) {
		return jsonx.J{"ok": false, "error": "request timed out: " + err.Error()}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return jsonx.J{"ok": false, "error": "connection failed: " + err.Error()}
	}
	return jsonx.J{"ok": false, "error": "request failed: " + err.Error()}
}

// IsTransportError reports whether an error string carries one of the two
// transport-shaped prefixes classifyTransportErr produces when the client
// itself didn't answer (timed out or refused), as opposed to an HTTP status
// error from a reachable client. Callers use it to set clientUnreachable only
// when the device is genuinely unreachable — never for a 4xx/5xx bind.
func IsTransportError(errStr string) bool {
	return strings.HasPrefix(errStr, "request timed out") || strings.HasPrefix(errStr, "connection failed")
}

// playerCmd mirrors _player_cmd: fire-and-report Companion command, always
// returning an {"ok": ...} dict rather than raising.
func playerCmd(client jsonx.J, path string, extra map[string]string) jsonx.J {
	params := url.Values{}
	params.Set("commandID", strconv.FormatInt(nextCommandID(), 10))
	params.Set("type", "video")
	for k, v := range extra {
		params.Set(k, v)
	}

	resp, body, err := companionGet(client, path, params)
	if err != nil {
		return classifyTransportErr(err)
	}
	// raise_for_status() only raises on 4xx/5xx -- 3xx is not an error.
	if resp.StatusCode >= 400 {
		return jsonx.J{"ok": false, "error": api.FormatHTTPError(resp.StatusCode, resp.Header.Get("Content-Type"), string(body), http.StatusText(resp.StatusCode))}
	}
	return jsonx.J{"ok": true}
}

// PlayerGet mirrors playback._player_get: GET from the client's Companion
// endpoint; parsed JSON or CompanionTransportError.
func PlayerGet(client jsonx.J, path string, extraParams map[string]string) (jsonx.J, error) {
	params := url.Values{}
	params.Set("commandID", strconv.FormatInt(nextCommandID(), 10))
	for k, v := range extraParams {
		params.Set(k, v)
	}

	resp, body, err := companionGet(client, path, params)
	if err != nil {
		return nil, &CompanionTransportError{Msg: err.Error()}
	}
	// raise_for_status() only raises on 4xx/5xx -- 3xx is not an error.
	if resp.StatusCode >= 400 {
		return nil, &CompanionTransportError{Msg: resp.Status}
	}
	if strings.TrimSpace(string(body)) == "" {
		return jsonx.J{}, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, &CompanionTransportError{Msg: err.Error()}
	}
	return parsed, nil
}

// getSession mirrors _get_session: {"state", "viewOffset"} for client's
// active session, or nil. Single /status/sessions fetch — both state (for
// unpause/repause around a seek) and viewOffset (for relative seek math)
// come from the same payload.
func getSession(client jsonx.J) jsonx.J {
	data, err := api.TryGet("/status/sessions", nil)
	if err != nil {
		return nil
	}
	machineID := client["machineIdentifier"]
	mc := jsonx.GetMap(data, "MediaContainer")
	for _, s := range jsonx.MapList(mc, "Metadata") {
		player := jsonx.GetMap(s, "Player")
		if player["machineIdentifier"] != machineID {
			continue
		}
		var viewOffset any
		if raw, ok := s["viewOffset"]; ok && raw != nil {
			switch v := raw.(type) {
			case float64:
				viewOffset = int(v)
			case int:
				viewOffset = v
			case int64:
				viewOffset = int(v)
			case json.Number:
				if f, err := v.Float64(); err == nil {
					viewOffset = int(f)
				}
			}
		}
		return jsonx.J{"state": player["state"], "viewOffset": viewOffset}
	}
	return nil
}

// GetServerMachineID mirrors playback._get_server_machine_id; "" on failure.
func GetServerMachineID() string {
	data, err := api.TryGet("/", nil)
	if err != nil {
		return ""
	}
	mc := jsonx.GetMap(data, "MediaContainer")
	if v, ok := mc["machineIdentifier"]; ok && jsonx.Truthy(v) {
		// Truthy guard: an explicit null must read as missing (Python's
		// .get() → None → falsy), not stringify into a garbage id.
		return jsonx.AsStr(v)
	}
	return ""
}

// Transport commands mirror their Python namesakes.
func Play(client jsonx.J) jsonx.J  { return playerCmd(client, "/player/playback/play", nil) }
func Pause(client jsonx.J) jsonx.J { return playerCmd(client, "/player/playback/pause", nil) }
func Stop(client jsonx.J) jsonx.J  { return playerCmd(client, "/player/playback/stop", nil) }
func StepForward(client jsonx.J) jsonx.J {
	return playerCmd(client, "/player/playback/stepForward", nil)
}
func StepBack(client jsonx.J) jsonx.J { return playerCmd(client, "/player/playback/stepBack", nil) }

// SetVolume mirrors playback.set_volume.
func SetVolume(client jsonx.J, level int) jsonx.J {
	return playerCmd(client, "/player/playback/setParameters", map[string]string{"volume": strconv.Itoa(level)})
}

// --- seek ------------------------------------------------------------------

var (
	relSeekRe = regexp.MustCompile(`^([+-])(\d+(?:\.\d+)?)([sm])$`)
	tsSeekRe  = regexp.MustCompile(`^(?:(\d+):)?(\d{1,2}):(\d{2})$`)
)

// sleep is a package var so tests can stub the paused-dance wait.
var sleep = time.Sleep

// Seek mirrors playback.seek including the paused-player resume->seek->
// re-pause dance and its 1s wait.
func Seek(client jsonx.J, position string, unpause bool) jsonx.J {
	position = strings.TrimSpace(position)

	rel := relSeekRe.FindStringSubmatch(position)
	ts := tsSeekRe.FindStringSubmatch(position)
	if rel == nil && ts == nil {
		return jsonx.J{"ok": false, "error": fmt.Sprintf("unrecognised position format: %s", jsonx.PyRepr(position))}
	}

	if ts != nil {
		h, m, s := ts[1], ts[2], ts[3]
		ss, _ := strconv.Atoi(s)
		mm, _ := strconv.Atoi(m)
		if ss >= 60 {
			return jsonx.J{"ok": false, "error": "invalid seek position: seconds must be < 60"}
		}
		if h != "" && mm >= 60 {
			return jsonx.J{"ok": false, "error": "invalid seek position: minutes must be < 60 when hours given"}
		}
	}

	// One /status/sessions fetch: relative seek needs viewOffset, every seek
	// needs the play/pause state to know whether to auto-resume+repause.
	var session jsonx.J
	if rel != nil || unpause {
		session = getSession(client)
	}

	var targetMs int
	if rel != nil {
		if session == nil || session["viewOffset"] == nil {
			return jsonx.J{"ok": false, "error": "could not determine current playback position"}
		}
		sign, valStr, unit := rel[1], rel[2], rel[3]
		val, _ := strconv.ParseFloat(valStr, 64)
		mult := 1000.0
		if unit == "m" {
			mult = 60000.0
		}
		deltaMs := int(val * mult)
		offset := session["viewOffset"].(int)
		if sign == "+" {
			targetMs = offset + deltaMs
		} else {
			targetMs = offset - deltaMs
		}
		if targetMs < 0 {
			targetMs = 0
		}
	} else {
		h, m, s := ts[1], ts[2], ts[3]
		mm, _ := strconv.Atoi(m)
		ss, _ := strconv.Atoi(s)
		if h != "" {
			hh, _ := strconv.Atoi(h)
			targetMs = (hh*3600 + mm*60 + ss) * 1000
		} else {
			targetMs = (mm*60 + ss) * 1000
		}
	}

	var state string
	if session != nil {
		state, _ = session["state"].(string)
	}
	wasPaused := unpause && state == "paused"

	if wasPaused {
		pre := playerCmd(client, "/player/playback/play", nil)
		if !jsonx.Truthy(pre["ok"]) {
			errStr, _ := pre["error"].(string)
			return jsonx.J{"ok": false, "error": "could not resume before seek: " + errStr}
		}
		sleep(1 * time.Second)
	}
	result := playerCmd(client, "/player/playback/seekTo", map[string]string{"offset": strconv.Itoa(targetMs)})
	if wasPaused && jsonx.Truthy(result["ok"]) {
		post := playerCmd(client, "/player/playback/pause", nil)
		if !jsonx.Truthy(post["ok"]) {
			errStr, _ := post["error"].(string)
			return jsonx.J{"ok": false, "error": "seeked but failed to restore pause state: " + errStr}
		}
	}
	return result
}

// --- playQueue / playMedia ---------------------------------------------------

// hostPort parses host and port from a server_url (http://host:port),
// defaulting the port to 32400 when unspecified.
func hostPort(serverURL string) (string, int) {
	port := 32400
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", port
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	return u.Hostname(), port
}

// PlayQueue mirrors playback.play_queue.
func PlayQueue(client jsonx.J, queueID, selectedItemID string) jsonx.J {
	serverID := GetServerMachineID()
	if serverID == "" {
		return jsonx.J{"ok": false, "error": "could not retrieve server machineIdentifier"}
	}
	cfg := config.Load()
	serverURL := config.StringOr(cfg, "server_url", config.Defaults["server_url"])
	address, port := hostPort(serverURL)
	return playerCmd(client, "/player/playback/playMedia", map[string]string{
		"key":                     "/playQueues/" + queueID,
		"playQueueID":             queueID,
		"playQueueSelectedItemID": selectedItemID,
		"machineIdentifier":       serverID,
		"address":                 address,
		"port":                    strconv.Itoa(port),
		"offset":                  "0",
	})
}

// PlayMedia mirrors playback.play_media.
func PlayMedia(client jsonx.J, ratingKey string) jsonx.J {
	serverID := GetServerMachineID()
	if serverID == "" {
		return jsonx.J{"ok": false, "error": "could not retrieve server machineIdentifier"}
	}
	cfg := config.Load()
	serverURL := config.StringOr(cfg, "server_url", config.Defaults["server_url"])
	address, port := hostPort(serverURL)
	key := "/library/metadata/" + ratingKey
	return playerCmd(client, "/player/playback/playMedia", map[string]string{
		"key":               key,
		"machineIdentifier": serverID,
		"address":           address,
		"port":              strconv.Itoa(port),
		"offset":            "0",
		"containerKey":      key,
	})
}
