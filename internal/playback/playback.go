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
	"github.com/corinthian/plexctl/internal/output"
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

// commandIDPath is the counter value file; commandIDLockPath is its dedicated
// lock. The lock is a SEPARATE, stable inode because the value file is rewritten
// via temp-file+rename (its inode changes every write) — flocking the value
// file itself would lock an inode that's about to be replaced. Both ride
// $PLEXCTL_CONFIG_DIR the same way queue state does.
func commandIDPath() string {
	return filepath.Join(config.Dir(), "commandid")
}

func commandIDLockPath() string {
	return filepath.Join(config.Dir(), "commandid.lock")
}

// nextPersistedCommandID computes and persists the next strictly-increasing
// commandID under an exclusive lock on commandid.lock, so IDs never collide
// across concurrent CLI processes. The next ID is floored above THREE things:
// the wall clock, the persisted value+1, and minExclusive+1 (the caller's
// in-memory high-water mark) — so a transient file failure that fell back to
// the in-memory counter can never reissue a consumed ID once the file recovers
// (finding 4). A corrupt or empty value file is treated as absent: the counter
// reseeds from the floors and rewrites (self-healing) instead of trapping in
// the permanent in-memory fallback (finding 3). The atomic temp+rename write is
// LOAD-BEARING, not hygiene: cross-process monotonicity rests entirely on the
// value file surviving intact, so it must never be left partial or empty
// (finding 5). Returns ok=false on any filesystem/lock failure, signalling the
// caller to fall back to the in-memory epoch seed.
func nextPersistedCommandID(minExclusive int64) (int64, bool) {
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return 0, false
	}
	lockFile, err := os.OpenFile(commandIDLockPath(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return 0, false
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return 0, false
	}
	defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) }()

	next := nowUnix()
	if minExclusive+1 > next {
		next = minExclusive + 1
	}
	// Only a parseable value raises the floor. A read error (absent file),
	// empty file, or corrupt (unparseable) contents all fall through as
	// "absent" — reseed from the floors above and rewrite.
	if b, rerr := os.ReadFile(commandIDPath()); rerr == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			if persisted, perr := strconv.ParseInt(s, 10, 64); perr == nil && persisted+1 > next {
				next = persisted + 1
			}
		}
	}
	// Atomic write: temp file + rename, so the value file is never observed
	// partial or empty (mirrors queuestate.writeAll). This is the sole
	// guarantor of cross-process monotonicity across a crash.
	tmp := commandIDPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(next, 10)), 0o600); err != nil {
		return 0, false
	}
	if err := os.Rename(tmp, commandIDPath()); err != nil {
		_ = os.Remove(tmp) // best-effort: don't leave a stale .tmp behind on a failed rename
		return 0, false
	}
	return next, true
}

func nextCommandID() int64 {
	commandIDMu.Lock()
	defer commandIDMu.Unlock()
	// Pass the in-memory high-water mark so the persisted path floors above any
	// ID a prior fallback issued but couldn't persist (0 when unseeded).
	if id, ok := nextPersistedCommandID(commandID); ok {
		// Shadow the persisted value so a later file failure continues from
		// here instead of reseeding from the clock.
		commandID = id
		commandIDSeeded = true
		return id
	}
	// Fallback: counter file unreachable — pure in-memory epoch seed
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
	httpClient := api.NewHTTPClient(time.Duration(api.DefaultTimeout()*float64(time.Second)), nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	// PMS library responses are legitimately large; 32 MiB just yields a
	// JSON parse error downstream on truncation, not a sentinel to handle.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return resp, nil, err
	}
	return resp, body, nil
}

// classifyTransportErr mirrors _player_cmd's exception ladder: a connect
// timeout must classify as a timeout before the generic connection-failed
// branch (ConnectTimeout subclasses both in requests). Returns an *api.Error
// so playerCmd can hand it straight to api.Classify — the v2 chokepoint for
// turning a transport shape into a coded CLIError (docs/error_model_v2.md
// §3's "playerCmd's three transport-error maps ... via api.Classify"). Kind
// carries through for parity with api.go's own classifyTransport even though
// api.Classify(_, TargetClient) doesn't currently branch on it; Message uses
// api.SanitizeError so no query string (which can carry the token) ever
// reaches the envelope.
func classifyTransportErr(err error) *api.Error {
	var ne net.Error
	if (errors.As(err, &ne) && ne.Timeout()) || errors.Is(err, context.DeadlineExceeded) {
		return &api.Error{Message: "request timed out: " + api.SanitizeError(err), Kind: "timeout"}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return &api.Error{Message: "connection failed: " + api.SanitizeError(err), Kind: "error"}
	}
	return &api.Error{Message: "request failed: " + api.SanitizeError(err), Kind: "error"}
}

// playerCmd mirrors _player_cmd: fire-and-report Companion command. v2
// (docs/error_model_v2.md §3, transport.go row): a transport-shaped failure
// classifies via api.Classify(_, api.TargetClient) — CodeClientUnreachable,
// exit 3, wake-the-device hint; an HTTP >= 400 response from the client
// itself becomes CodeHTTPError carrying the status. Success still returns a
// plain {"ok": true} result.
func playerCmd(client jsonx.J, path string, extra map[string]string) (jsonx.J, *output.CLIError) {
	params := url.Values{}
	params.Set("commandID", strconv.FormatInt(nextCommandID(), 10))
	params.Set("type", "video")
	for k, v := range extra {
		params.Set(k, v)
	}

	resp, body, err := companionGet(client, path, params)
	if err != nil {
		return nil, api.Classify(classifyTransportErr(err), api.TargetClient)
	}
	// raise_for_status() only raises on 4xx/5xx -- 3xx is not an error.
	if resp.StatusCode >= 400 {
		msg := api.FormatHTTPError(resp.StatusCode, resp.Header.Get("Content-Type"), string(body), http.StatusText(resp.StatusCode))
		return nil, output.Err(output.CodeHTTPError, msg).WithHTTPStatus(resp.StatusCode)
	}
	return jsonx.J{"ok": true}, nil
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

// EngagePollDelay paces Play's idle-play engagement poll (v2 NOT_APPLIED
// invariant, docs/error_model_v2.md §5). Exported — unlike the paused-dance
// `sleep` seam above, the idle-play golden lives in package commands_test
// (internal/commands), a different package, so it needs its own settable
// var rather than reusing an unexported one; production never overrides it.
var EngagePollDelay = time.Second

// Play mirrors playback.play, with the v2 idle-play invariant layered on
// top (contract §5, decision locked: no auto-bootstrap). A Companion accept
// only proves the client's listener answered — incidents already established
// (see internal/queue/verify.go) that the Plex app can accept a command it
// never acts on. So after the play command is accepted, poll the client's
// own session up to 2 tries (immediate, then one more after
// EngagePollDelay): if either try shows the session non-idle, report success
// as before; if both show idle/no session, the accept was a no-op — bare
// `play` only resumes, it can never start an idle client — so report
// NOT_APPLIED (exit 6) instead of a false ok:true.
//
// This verification is local to Play ONLY. seek's pre-seek resume
// (playerCmd(client, "/player/playback/play", nil) called directly, not
// through Play) and queue flows are NOT subject to it — they have their own
// verification (queue.ConfirmEngaged) or none by design (contract §5's
// exemption list).
func Play(client jsonx.J) (jsonx.J, *output.CLIError) {
	result, cliErr := playerCmd(client, "/player/playback/play", nil)
	if cliErr != nil {
		return nil, cliErr
	}
	for try := 0; try < 2; try++ {
		if try > 0 {
			sleep(EngagePollDelay)
		}
		if session := getSession(client); session != nil {
			if state, _ := session["state"].(string); state != "" && state != "idle" {
				return result, nil
			}
		}
	}
	return nil, output.Err(output.CodeNotApplied,
		"client accepted play but nothing started — play only resumes; it cannot start an idle client").
		WithHint("start items with: plexctl play-media RATING_KEY")
}

func Pause(client jsonx.J) (jsonx.J, *output.CLIError) {
	return playerCmd(client, "/player/playback/pause", nil)
}
func Stop(client jsonx.J) (jsonx.J, *output.CLIError) {
	return playerCmd(client, "/player/playback/stop", nil)
}
func StepForward(client jsonx.J) (jsonx.J, *output.CLIError) {
	return playerCmd(client, "/player/playback/stepForward", nil)
}
func StepBack(client jsonx.J) (jsonx.J, *output.CLIError) {
	return playerCmd(client, "/player/playback/stepBack", nil)
}

// SetVolume is gone (v2): the Apple TV Companion listener accepts
// setParameters volume commands but silently ignores them, so the HTTP call
// served no purpose — the skill's volume refusal moves into the binary as
// an immediate CodeUnsupported FailErr in internal/commands/transport.go,
// with no client resolution and no Companion round trip at all.

// --- seek ------------------------------------------------------------------

var (
	relSeekRe = regexp.MustCompile(`^([+-])(\d+(?:\.\d+)?)([sm])$`)
	tsSeekRe  = regexp.MustCompile(`^(?:(\d+):)?(\d{1,2}):(\d{2})$`)
)

// sleep is a package var so tests can stub the paused-dance wait.
var sleep = time.Sleep

// Seek mirrors playback.seek including the paused-player resume->seek->
// re-pause dance and its 1s wait. v2 (docs/error_model_v2.md §2/§3/§6):
// position-parse failures are CodeBadRequest; a relative seek with no
// current session/viewOffset is CodeNothingPlaying; a failure anywhere in
// the resume/seek/re-pause sequence is CodeSeekFailed carrying
// data.seeked/data.repaused so a caller can tell how far it got; on success
// the envelope gains "playState" (playing/paused) — the state the sequence
// left the client in.
func Seek(client jsonx.J, position string, unpause bool) (jsonx.J, *output.CLIError) {
	position = strings.TrimSpace(position)

	rel := relSeekRe.FindStringSubmatch(position)
	ts := tsSeekRe.FindStringSubmatch(position)
	if rel == nil && ts == nil {
		return nil, output.Err(output.CodeBadRequest, fmt.Sprintf("unrecognised position format: %s", jsonx.PyRepr(position)))
	}

	if ts != nil {
		h, m, s := ts[1], ts[2], ts[3]
		ss, _ := strconv.Atoi(s)
		mm, _ := strconv.Atoi(m)
		if ss >= 60 {
			return nil, output.Err(output.CodeBadRequest, "invalid seek position: seconds must be < 60")
		}
		if h != "" && mm >= 60 {
			return nil, output.Err(output.CodeBadRequest, "invalid seek position: minutes must be < 60 when hours given")
		}
	}

	// One /status/sessions fetch: relative seek needs viewOffset, every seek
	// needs the play/pause state to know whether to auto-resume+repause (and,
	// v2, to report the resulting playState on success).
	var session jsonx.J
	if rel != nil || unpause {
		session = getSession(client)
	}

	var targetMs int
	if rel != nil {
		if session == nil || session["viewOffset"] == nil {
			return nil, output.Err(output.CodeNothingPlaying, "could not determine current playback position").
				WithHint("nothing to seek — start playback first")
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
		_, cliErr := playerCmd(client, "/player/playback/play", nil)
		if cliErr != nil {
			return nil, output.Err(output.CodeSeekFailed, "could not resume before seek: "+cliErr.Message).
				WithHint("try again").
				WithData("seeked", false).
				WithData("repaused", false)
		}
		sleep(1 * time.Second)
	}
	result, cliErr := playerCmd(client, "/player/playback/seekTo", map[string]string{"offset": strconv.Itoa(targetMs)})
	if cliErr != nil {
		return nil, output.Err(output.CodeSeekFailed, "seek failed: "+cliErr.Message).
			WithHint("try again").
			WithData("seeked", false).
			WithData("repaused", false)
	}
	// playState: the dance (when it ran) always restores pause; otherwise the
	// client is left exactly as it was found — paused only when the caller
	// asked to leave it alone (--no-unpause) on a session already paused.
	// The one gap (absolute position + --no-unpause + no prior session read)
	// has no observed state to report and defaults to "playing".
	playState := "playing"
	if wasPaused {
		_, cliErr := playerCmd(client, "/player/playback/pause", nil)
		if cliErr != nil {
			return nil, output.Err(output.CodeSeekFailed, "seeked but failed to restore pause state: "+cliErr.Message).
				WithData("seeked", true).
				WithData("repaused", false)
		}
		playState = "paused"
	} else if state == "paused" {
		playState = "paused"
	}
	result["playState"] = playState
	return result, nil
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

// PlayQueue mirrors playback.play_queue. selectedItemID is omitted from the
// request entirely when nil/empty (jsonx.AsStr(nil)'s "None" sentinel is
// useful for display but must never reach a request parameter — a
// Python-era queue_state.json with a null selectedItemID can still reach
// this call via the saved/staged Start path).
func PlayQueue(client jsonx.J, queueID, selectedItemID string) (jsonx.J, *output.CLIError) {
	serverID := GetServerMachineID()
	if serverID == "" {
		return nil, output.Err(output.CodeInternal, "could not retrieve server machineIdentifier")
	}
	cfg := config.Load()
	serverURL := config.StringOr(cfg, "server_url", config.Defaults["server_url"])
	address, port := hostPort(serverURL)
	params := map[string]string{
		"key":               "/playQueues/" + queueID,
		"playQueueID":       queueID,
		"machineIdentifier": serverID,
		"address":           address,
		"port":              strconv.Itoa(port),
		"offset":            "0",
	}
	if selectedItemID != "" && selectedItemID != "None" {
		params["playQueueSelectedItemID"] = selectedItemID
	}
	return playerCmd(client, "/player/playback/playMedia", params)
}

// PlayMedia mirrors playback.play_media.
func PlayMedia(client jsonx.J, ratingKey string) (jsonx.J, *output.CLIError) {
	serverID := GetServerMachineID()
	if serverID == "" {
		return nil, output.Err(output.CodeInternal, "could not retrieve server machineIdentifier")
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
