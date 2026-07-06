package playback

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/testutil"
)

// --- fakes -------------------------------------------------------------

type companionCall struct {
	path   string
	query  url.Values
	header http.Header
}

// fakeCompanion stands in for the Apple TV's Companion HTTP endpoint. It
// records every request and can be told to fail specific paths.
type fakeCompanion struct {
	mu    sync.Mutex
	calls []companionCall
	fail  map[string]int
}

func newFakeCompanion(t *testing.T) (*fakeCompanion, *httptest.Server) {
	t.Helper()
	fc := &fakeCompanion{fail: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fc.mu.Lock()
		fc.calls = append(fc.calls, companionCall{path: r.URL.Path, query: r.URL.Query(), header: r.Header.Clone()})
		status, failing := fc.fail[r.URL.Path]
		fc.mu.Unlock()
		if failing {
			http.Error(w, "boom", status)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return fc, srv
}

func (fc *fakeCompanion) failPath(path string, status int) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.fail[path] = status
}

func (fc *fakeCompanion) snapshot() []companionCall {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	out := make([]companionCall, len(fc.calls))
	copy(out, fc.calls)
	return out
}

func (fc *fakeCompanion) paths() []string {
	calls := fc.snapshot()
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.path
	}
	return out
}

func (fc *fakeCompanion) last() companionCall {
	calls := fc.snapshot()
	return calls[len(calls)-1]
}

func fakeClient(baseurl string) jsonx.J {
	return jsonx.J{"machineIdentifier": "atv-1", "baseurl": baseurl}
}

// newFakePMS serves /status/sessions (empty Metadata when session is nil)
// and / (machineIdentifier omitted when serverMachineID is "").
func newFakePMS(t *testing.T, session jsonx.J, serverMachineID string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status/sessions":
			meta := []any{}
			if session != nil {
				meta = append(meta, jsonx.J{
					"viewOffset": session["viewOffset"],
					"Player": jsonx.J{
						"machineIdentifier": "atv-1",
						"state":             session["state"],
					},
				})
			}
			b, _ := json.Marshal(jsonx.J{"MediaContainer": jsonx.J{"Metadata": meta}})
			w.Write(b)
		case "/":
			mc := jsonx.J{}
			if serverMachineID != "" {
				mc["machineIdentifier"] = serverMachineID
			}
			b, _ := json.Marshal(jsonx.J{"MediaContainer": mc})
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- transport commands: happy path, commandID, error classification -------

func TestPlayerCmdHappyPathHeadersAndParams(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	testutil.Setup(t, "http://pms.test:32400")
	client := fakeClient(csrv.URL)

	result := Play(client)
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	call := fc.last()
	if call.query.Get("type") != "video" {
		t.Fatalf("missing type=video: %v", call.query)
	}
	if call.query.Get("commandID") == "" {
		t.Fatalf("missing commandID: %v", call.query)
	}
	if call.header.Get("X-Plex-Target-Client-Identifier") != "atv-1" {
		t.Fatalf("missing target client id header: %v", call.header)
	}
}

func TestCommandIDMonotonicAcrossCalls(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	testutil.Setup(t, "http://pms.test:32400")
	client := fakeClient(csrv.URL)

	Play(client)
	Pause(client)
	calls := fc.snapshot()
	if len(calls) != 2 {
		t.Fatalf("want 2 calls, got %d", len(calls))
	}
	id1, _ := strconv.ParseInt(calls[0].query.Get("commandID"), 10, 64)
	id2, _ := strconv.ParseInt(calls[1].query.Get("commandID"), 10, 64)
	if id2 <= id1 {
		t.Fatalf("commandID not strictly increasing: %d -> %d", id1, id2)
	}
}

// resetCommandIDState simulates a fresh process: the in-memory fallback state
// is gone, but any on-disk counter file persists.
func resetCommandIDState() {
	commandIDMu.Lock()
	defer commandIDMu.Unlock()
	commandID, commandIDSeeded = 0, false
}

// B5: two sequential "processes" sharing the counter file never repeat an ID,
// even with the wall clock frozen — the persisted counter alone forces the
// increment.
func TestCommandIDPersistedMonotonicAcrossProcessesFrozenClock(t *testing.T) {
	testutil.Setup(t, "http://unused")
	oldNow := nowUnix
	nowUnix = func() int64 { return 1000 }
	t.Cleanup(func() { nowUnix = oldNow; resetCommandIDState() })

	resetCommandIDState()
	id1 := nextCommandID()
	resetCommandIDState() // fresh process; file survives
	id2 := nextCommandID()

	if id1 != 1000 {
		t.Fatalf("id1 = %d, want 1000 (epoch seed on empty counter file)", id1)
	}
	if id2 != 1001 {
		t.Fatalf("id2 = %d, want 1001 (persisted+1 despite frozen clock)", id2)
	}
}

// C3 (finding 3): a corrupt counter file is REPAIRED, not permanently fallen
// back on. The next call returns a valid ID, rewrites the file with it, and
// subsequent calls strictly increase — no permanent in-memory fallback that
// would reintroduce same-second collisions across processes.
func TestCommandIDCorruptFileSelfHeals(t *testing.T) {
	dir := testutil.Setup(t, "http://unused")
	if err := os.WriteFile(filepath.Join(dir, "commandid"), []byte("not-a-number"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldNow := nowUnix
	nowUnix = func() int64 { return 5000 }
	t.Cleanup(func() { nowUnix = oldNow; resetCommandIDState() })

	resetCommandIDState()
	id1 := nextCommandID()
	id2 := nextCommandID()
	if id1 <= 0 || id2 <= id1 {
		t.Fatalf("not monotonic after corrupt file: id1=%d id2=%d", id1, id2)
	}
	// Healed: the file now holds a parseable number (the last issued ID), so the
	// corruption does not survive into the next process.
	raw, err := os.ReadFile(filepath.Join(dir, "commandid"))
	if err != nil {
		t.Fatalf("reading healed commandid: %v", err)
	}
	healed, perr := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if perr != nil {
		t.Fatalf("commandid not healed to a number: %q (%v)", raw, perr)
	}
	if healed != id2 {
		t.Fatalf("healed file = %d, want last issued id %d", healed, id2)
	}
}

// C3 (finding 4): the persisted next-ID is floored above the caller's in-memory
// high-water mark, so a transient file failure that already issued IDs from the
// in-memory fallback can never have one re-issued once the file recovers.
// Tested directly on the primitive with a frozen low clock so only the floors
// are in play.
func TestCommandIDFloorsAboveInMemoryHighWater(t *testing.T) {
	dir := testutil.Setup(t, "http://unused")
	oldNow := nowUnix
	nowUnix = func() int64 { return 50 }
	t.Cleanup(func() { nowUnix = oldNow; resetCommandIDState() })

	// Persisted value 100 → first floored to max(clock 50, 100+1, 0+1) = 101.
	if err := os.WriteFile(filepath.Join(dir, "commandid"), []byte("100"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, ok := nextPersistedCommandID(0)
	if !ok || first != 101 {
		t.Fatalf("first = %d ok=%v, want 101", first, ok)
	}
	// A prior fallback issued up to 200 in-memory without persisting. The file
	// still says 101, but the floor must lift next above 200 — NOT reissue 102.
	next, ok := nextPersistedCommandID(200)
	if !ok || next != 201 {
		t.Fatalf("next = %d ok=%v, want 201 (floored above in-memory 200, not persisted+1)", next, ok)
	}
}

// C3 (finding 5, kill-window): if a crash leaves the value file empty, the
// counter reseeds from max(clock, in-memory) and self-heals. We assert ONLY
// self-heal and process-local monotonicity. We deliberately do NOT assert the
// reseeded value is globally safe: at the reseed instant nothing can recover
// IDs a prior same-second process issued in-memory, so one cross-process
// collision remains possible here — an accepted, documented residual (see the
// locked design decision). Do not "fix" this test into a global guarantee.
func TestCommandIDEmptyFileKillWindowSelfHeals(t *testing.T) {
	dir := testutil.Setup(t, "http://unused")
	if err := os.WriteFile(filepath.Join(dir, "commandid"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	oldNow := nowUnix
	nowUnix = func() int64 { return 7000 }
	t.Cleanup(func() { nowUnix = oldNow; resetCommandIDState() })

	resetCommandIDState()
	id1 := nextCommandID()
	id2 := nextCommandID()
	if id1 != 7000 {
		t.Fatalf("id1 = %d, want 7000 (reseed from frozen clock on empty file)", id1)
	}
	if id2 <= id1 {
		t.Fatalf("process-local monotonicity broken: id1=%d id2=%d", id1, id2)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "commandid"))
	if _, perr := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); perr != nil {
		t.Fatalf("empty file not healed to a number: %q", raw)
	}
}

// B5: the counter file honors $PLEXCTL_CONFIG_DIR (same override queue state
// uses), so tests and sandboxes stay isolated from the real config dir.
func TestCommandIDRespectsConfigDir(t *testing.T) {
	dir := testutil.Setup(t, "http://unused")
	t.Cleanup(resetCommandIDState)
	resetCommandIDState()
	nextCommandID()
	if _, err := os.Stat(filepath.Join(dir, "commandid")); err != nil {
		t.Fatalf("commandid not written under PLEXCTL_CONFIG_DIR: %v", err)
	}
}

func TestConnectionFailedClassification(t *testing.T) {
	testutil.Setup(t, "http://pms.test:32400")
	client := fakeClient("http://127.0.0.1:1") // nothing listens here

	result := Play(client)
	if jsonx.Truthy(result["ok"]) {
		t.Fatalf("want failure, got %#v", result)
	}
	errStr, _ := result["error"].(string)
	if !strings.HasPrefix(errStr, "connection failed:") {
		t.Fatalf("want 'connection failed:' prefix, got %q", errStr)
	}
}

func TestTimeoutClassification(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	t.Cleanup(slow.Close)
	testutil.Setup(t, "http://pms.test:32400")
	api.SetTimeoutOverride(0.05)
	t.Cleanup(api.ClearTimeoutOverride)

	result := Play(fakeClient(slow.URL))
	errStr, _ := result["error"].(string)
	if !strings.HasPrefix(errStr, "request timed out:") {
		t.Fatalf("want 'request timed out:' prefix, got %q", errStr)
	}
}

func TestHTTPErrorClassification(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	fc.failPath("/player/playback/play", 404)
	testutil.Setup(t, "http://pms.test:32400")

	result := Play(fakeClient(csrv.URL))
	errStr, _ := result["error"].(string)
	if !strings.HasPrefix(errStr, "HTTP 404:") {
		t.Fatalf("want 'HTTP 404:' prefix, got %q", errStr)
	}
}

// --- PlayerGet ---------------------------------------------------------

func TestPlayerGetParsesJSON(t *testing.T) {
	companion := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"foo": "bar"}`))
	}))
	t.Cleanup(companion.Close)
	testutil.Setup(t, "http://pms.test:32400")

	result, err := PlayerGet(fakeClient(companion.URL), "/player/timeline/poll", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["foo"] != "bar" {
		t.Fatalf("got %#v", result)
	}
}

func TestPlayerGetEmptyBodyIsEmptyMap(t *testing.T) {
	companion := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(companion.Close)
	testutil.Setup(t, "http://pms.test:32400")

	result, err := PlayerGet(fakeClient(companion.URL), "/player/timeline/poll", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("want empty map, got %#v", result)
	}
}

func TestPlayerGetTransportErrorWraps(t *testing.T) {
	testutil.Setup(t, "http://pms.test:32400")

	_, err := PlayerGet(fakeClient("http://127.0.0.1:1"), "/player/timeline/poll", nil)
	if err == nil {
		t.Fatal("want error")
	}
	if _, ok := err.(*CompanionTransportError); !ok {
		t.Fatalf("want *CompanionTransportError, got %T", err)
	}
}

// --- seek ----------------------------------------------------------------

func TestSeekRejectsBadFormat(t *testing.T) {
	result := Seek(fakeClient("http://unused"), "not-a-time", true)
	if got := result["error"]; got != "unrecognised position format: 'not-a-time'" {
		t.Fatalf("error = %q", got)
	}
	if jsonx.Truthy(result["ok"]) {
		t.Fatal("want ok=false")
	}
}

func TestSeekRejectsSecondsGE60(t *testing.T) {
	result := Seek(fakeClient("http://unused"), "99:99", true)
	if got := result["error"]; got != "invalid seek position: seconds must be < 60" {
		t.Fatalf("error = %q", got)
	}
}

func TestSeekRejectsMinutesGE60WithHours(t *testing.T) {
	result := Seek(fakeClient("http://unused"), "1:99:30", true)
	if got := result["error"]; got != "invalid seek position: minutes must be < 60 when hours given" {
		t.Fatalf("error = %q", got)
	}
}

func TestSeekAcceptsHighMinutesWithoutHours(t *testing.T) {
	// mm:ss with mm >= 60 stays valid -- only enforced when hours are given.
	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, jsonx.J{"state": "playing", "viewOffset": 0}, "server-mid")
	testutil.Setup(t, pms.URL)

	result := Seek(fakeClient(csrv.URL), "99:30", true)
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	seekOffset(t, fc, "5970000") // (99*60+30)*1000
}

func seekOffset(t *testing.T, fc *fakeCompanion, want string) {
	t.Helper()
	for _, c := range fc.snapshot() {
		if strings.HasSuffix(c.path, "/seekTo") {
			if got := c.query.Get("offset"); got != want {
				t.Fatalf("seekTo offset = %s, want %s", got, want)
			}
			return
		}
	}
	t.Fatal("no seekTo call recorded")
}

func TestSeekAbsoluteMMSS(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, jsonx.J{"state": "playing", "viewOffset": 0}, "server-mid")
	testutil.Setup(t, pms.URL)

	result := Seek(fakeClient(csrv.URL), "1:30", true)
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	seekOffset(t, fc, "90000")
}

func TestSeekAbsoluteHHMMSS(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, jsonx.J{"state": "playing", "viewOffset": 0}, "server-mid")
	testutil.Setup(t, pms.URL)

	result := Seek(fakeClient(csrv.URL), "1:00:00", true)
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	seekOffset(t, fc, "3600000")
}

func TestSeekRelativePlusSeconds(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, jsonx.J{"state": "playing", "viewOffset": 60000}, "server-mid")
	testutil.Setup(t, pms.URL)

	result := Seek(fakeClient(csrv.URL), "+30s", true)
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	seekOffset(t, fc, "90000")
}

func TestSeekRelativeMinusMinuteClampsToZero(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, jsonx.J{"state": "playing", "viewOffset": 60000}, "server-mid")
	testutil.Setup(t, pms.URL)

	result := Seek(fakeClient(csrv.URL), "-1m", true)
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	seekOffset(t, fc, "0")
}

func TestSeekRelativeNoSession(t *testing.T) {
	_, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, nil, "server-mid") // no matching session
	testutil.Setup(t, pms.URL)

	result := Seek(fakeClient(csrv.URL), "+30s", true)
	if got := result["error"]; got != "could not determine current playback position" {
		t.Fatalf("error = %q", got)
	}
}

func TestSeekPausedDanceOrdering(t *testing.T) {
	oldSleep := sleep
	sleep = func(time.Duration) {}
	t.Cleanup(func() { sleep = oldSleep })

	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, jsonx.J{"state": "paused", "viewOffset": 0}, "server-mid")
	testutil.Setup(t, pms.URL)

	result := Seek(fakeClient(csrv.URL), "1:30", true)
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	got := fc.paths()
	want := []string{"/player/playback/play", "/player/playback/seekTo", "/player/playback/pause"}
	if !equalStrings(got, want) {
		t.Fatalf("sequence = %v, want %v", got, want)
	}
}

func TestSeekNoUnpauseSkipsDance(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	// No PMS server needed: unpause=false + absolute position never fetches
	// a session.
	testutil.Setup(t, "http://pms.test:32400")

	result := Seek(fakeClient(csrv.URL), "1:30", false)
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	got := fc.paths()
	want := []string{"/player/playback/seekTo"}
	if !equalStrings(got, want) {
		t.Fatalf("sequence = %v, want %v", got, want)
	}
}

func TestSeekResumeFailureMessage(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	fc.failPath("/player/playback/play", 500)
	pms := newFakePMS(t, jsonx.J{"state": "paused", "viewOffset": 0}, "server-mid")
	testutil.Setup(t, pms.URL)

	result := Seek(fakeClient(csrv.URL), "1:30", true)
	errStr, _ := result["error"].(string)
	if !strings.HasPrefix(errStr, "could not resume before seek: ") {
		t.Fatalf("error = %q", errStr)
	}
	for _, p := range fc.paths() {
		if strings.HasSuffix(p, "/seekTo") {
			t.Fatal("seekTo must not be called after a resume failure")
		}
	}
}

func TestSeekRepauseFailureMessage(t *testing.T) {
	oldSleep := sleep
	sleep = func(time.Duration) {}
	t.Cleanup(func() { sleep = oldSleep })

	fc, csrv := newFakeCompanion(t)
	fc.failPath("/player/playback/pause", 500)
	pms := newFakePMS(t, jsonx.J{"state": "paused", "viewOffset": 0}, "server-mid")
	testutil.Setup(t, pms.URL)

	result := Seek(fakeClient(csrv.URL), "1:30", true)
	errStr, _ := result["error"].(string)
	if !strings.HasPrefix(errStr, "seeked but failed to restore pause state: ") {
		t.Fatalf("error = %q", errStr)
	}
}

// --- PlayMedia / PlayQueue -------------------------------------------------

func TestPlayMediaAddressPortAndKeyShape(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, nil, "server-mid")
	testutil.Setup(t, pms.URL)

	result := PlayMedia(fakeClient(csrv.URL), "12345")
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	call := fc.last()
	pmsURL, _ := url.Parse(pms.URL)
	if got := call.query.Get("address"); got != pmsURL.Hostname() {
		t.Fatalf("address = %s, want %s", got, pmsURL.Hostname())
	}
	if got := call.query.Get("port"); got != pmsURL.Port() {
		t.Fatalf("port = %s, want %s", got, pmsURL.Port())
	}
	if got := call.query.Get("key"); got != "/library/metadata/12345" {
		t.Fatalf("key = %s", got)
	}
	if got := call.query.Get("containerKey"); got != "/library/metadata/12345" {
		t.Fatalf("containerKey = %s", got)
	}
	if got := call.query.Get("machineIdentifier"); got != "server-mid" {
		t.Fatalf("machineIdentifier = %s", got)
	}
	if got := call.query.Get("offset"); got != "0" {
		t.Fatalf("offset = %s", got)
	}
	if call.query.Has("playQueueID") {
		t.Fatal("playQueueID must not be present for playMedia")
	}
}

func TestPlayQueueKeyShapeAndParams(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, nil, "server-mid")
	testutil.Setup(t, pms.URL)

	result := PlayQueue(fakeClient(csrv.URL), "5535", "42687")
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	call := fc.last()
	if got := call.query.Get("key"); got != "/playQueues/5535" {
		t.Fatalf("key = %s", got)
	}
	if got := call.query.Get("playQueueID"); got != "5535" {
		t.Fatalf("playQueueID = %s", got)
	}
	if got := call.query.Get("playQueueSelectedItemID"); got != "42687" {
		t.Fatalf("playQueueSelectedItemID = %s", got)
	}
	if call.query.Has("containerKey") {
		t.Fatal("containerKey must not be present for playQueue")
	}
}

func TestPlayQueueMissingServerMachineID(t *testing.T) {
	_, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, nil, "") // "/" carries no machineIdentifier
	testutil.Setup(t, pms.URL)

	result := PlayQueue(fakeClient(csrv.URL), "5535", "42687")
	if got := result["error"]; got != "could not retrieve server machineIdentifier" {
		t.Fatalf("error = %q", got)
	}
}

func TestPlayMediaMissingServerMachineID(t *testing.T) {
	_, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, nil, "")
	testutil.Setup(t, pms.URL)

	result := PlayMedia(fakeClient(csrv.URL), "12345")
	if got := result["error"]; got != "could not retrieve server machineIdentifier" {
		t.Fatalf("error = %q", got)
	}
}
