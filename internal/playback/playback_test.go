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
	"github.com/corinthian/plexctl/internal/output"
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

// TestPlayerCmdHappyPathHeadersAndParams exercises playerCmd's generic
// header/param shape via Pause rather than Play: Play (v2) layers an
// idle-play engagement poll on top (see TestPlayEngagedFirstPollSucceeds /
// TestPlayIdleClientReportsNotApplied below), which is irrelevant to what
// this test checks and would otherwise need PMS session wiring just to
// avoid a spurious NOT_APPLIED.
func TestPlayerCmdHappyPathHeadersAndParams(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	testutil.Setup(t, "http://pms.test:32400")
	client := fakeClient(csrv.URL)

	result, cliErr := Pause(client)
	if cliErr != nil {
		t.Fatalf("want no error, got %#v", cliErr)
	}
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

	Pause(client)
	Stop(client)
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

// TestNextPersistedCommandIDRemovesTmpFileOnRenameFailure pins W13: a
// stale commandid.tmp left behind on a failed rename would sit on a fixed
// filename that could collide with or mask the next write's own temp file.
// Force the rename to fail by pre-creating a non-empty directory at the
// destination path.
func TestNextPersistedCommandIDRemovesTmpFileOnRenameFailure(t *testing.T) {
	dir := testutil.Setup(t, "http://unused")
	if err := os.Mkdir(commandIDPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commandIDPath(), "occupied"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resetCommandIDState)

	_, ok := nextPersistedCommandID(0)
	if ok {
		t.Fatal("expected ok=false after a rename failure")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "commandid.tmp")); !os.IsNotExist(statErr) {
		t.Fatalf("leftover commandid.tmp after failed rename: statErr=%v", statErr)
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

	result, cliErr := Play(client)
	if result != nil {
		t.Fatalf("want nil result on failure, got %#v", result)
	}
	if cliErr == nil {
		t.Fatal("want a CLIError")
	}
	if cliErr.Code != output.CodeClientUnreachable {
		t.Fatalf("code = %q, want %q", cliErr.Code, output.CodeClientUnreachable)
	}
	if !strings.HasPrefix(cliErr.Message, "connection failed:") {
		t.Fatalf("want 'connection failed:' prefix, got %q", cliErr.Message)
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

	_, cliErr := Play(fakeClient(slow.URL))
	if cliErr == nil {
		t.Fatal("want a CLIError")
	}
	if cliErr.Code != output.CodeClientUnreachable {
		t.Fatalf("code = %q, want %q", cliErr.Code, output.CodeClientUnreachable)
	}
	if !strings.HasPrefix(cliErr.Message, "request timed out:") {
		t.Fatalf("want 'request timed out:' prefix, got %q", cliErr.Message)
	}
}

func TestHTTPErrorClassification(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	fc.failPath("/player/playback/play", 404)
	testutil.Setup(t, "http://pms.test:32400")

	_, cliErr := Play(fakeClient(csrv.URL))
	if cliErr == nil {
		t.Fatal("want a CLIError")
	}
	if cliErr.Code != output.CodeHTTPError {
		t.Fatalf("code = %q, want %q", cliErr.Code, output.CodeHTTPError)
	}
	if cliErr.HTTPStatus != 404 {
		t.Fatalf("http status = %d, want 404", cliErr.HTTPStatus)
	}
	if !strings.HasPrefix(cliErr.Message, "HTTP 404:") {
		t.Fatalf("want 'HTTP 404:' prefix, got %q", cliErr.Message)
	}
}

// --- Play: idle-play NOT_APPLIED invariant (v2, docs/error_model_v2.md §5) -

// TestPlayEngagedFirstPollSucceeds covers the common case: the client is
// already showing a non-idle session on the immediate (no-sleep) first poll.
func TestPlayEngagedFirstPollSucceeds(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, jsonx.J{"state": "playing", "viewOffset": 5000}, "server-mid")
	testutil.Setup(t, pms.URL)

	result, cliErr := Play(fakeClient(csrv.URL))
	if cliErr != nil {
		t.Fatalf("want no error, got %#v", cliErr)
	}
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	if got := fc.paths(); len(got) != 1 || got[0] != "/player/playback/play" {
		t.Fatalf("companion calls = %v, want exactly one play", got)
	}
}

// TestPlayEngagedOnSecondPollSucceeds proves the loop actually retries: the
// first /status/sessions read shows nothing, the second (after
// EngagePollDelay) shows a session — success, not NOT_APPLIED.
func TestPlayEngagedOnSecondPollSucceeds(t *testing.T) {
	oldDelay := EngagePollDelay
	EngagePollDelay = 0
	t.Cleanup(func() { EngagePollDelay = oldDelay })

	_, csrv := newFakeCompanion(t)
	sessionCalls := 0
	pms := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/status/sessions" {
			http.NotFound(w, r)
			return
		}
		sessionCalls++
		meta := []any{}
		if sessionCalls >= 2 {
			meta = append(meta, jsonx.J{
				"viewOffset": 1000,
				"Player":     jsonx.J{"machineIdentifier": "atv-1", "state": "playing"},
			})
		}
		b, _ := json.Marshal(jsonx.J{"MediaContainer": jsonx.J{"Metadata": meta}})
		w.Write(b)
	}))
	t.Cleanup(pms.Close)
	testutil.Setup(t, pms.URL)

	result, cliErr := Play(fakeClient(csrv.URL))
	if cliErr != nil {
		t.Fatalf("want no error, got %#v", cliErr)
	}
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	if sessionCalls < 2 {
		t.Fatalf("want at least 2 session polls, got %d", sessionCalls)
	}
}

// TestPlayIdleClientReportsNotApplied is the NOT_APPLIED path: the Companion
// accept succeeds but both session polls come back idle/absent, so bare
// `play` must not report a false ok:true.
func TestPlayIdleClientReportsNotApplied(t *testing.T) {
	oldDelay := EngagePollDelay
	EngagePollDelay = 0
	t.Cleanup(func() { EngagePollDelay = oldDelay })

	_, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, nil, "server-mid") // no session at all -- idle
	testutil.Setup(t, pms.URL)

	result, cliErr := Play(fakeClient(csrv.URL))
	if result != nil {
		t.Fatalf("want nil result, got %#v", result)
	}
	if cliErr == nil {
		t.Fatal("want a CLIError")
	}
	if cliErr.Code != output.CodeNotApplied {
		t.Fatalf("code = %q, want %q", cliErr.Code, output.CodeNotApplied)
	}
	if cliErr.Hint != "start items with: plexctl play-media RATING_KEY" {
		t.Fatalf("hint = %q", cliErr.Hint)
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
	_, cliErr := Seek(fakeClient("http://unused"), "not-a-time", true)
	if cliErr == nil {
		t.Fatal("want a CLIError")
	}
	if cliErr.Code != output.CodeBadRequest {
		t.Fatalf("code = %q, want %q", cliErr.Code, output.CodeBadRequest)
	}
	if cliErr.Message != "unrecognised position format: 'not-a-time'" {
		t.Fatalf("message = %q", cliErr.Message)
	}
}

func TestSeekRejectsSecondsGE60(t *testing.T) {
	_, cliErr := Seek(fakeClient("http://unused"), "99:99", true)
	if cliErr == nil {
		t.Fatal("want a CLIError")
	}
	if cliErr.Code != output.CodeBadRequest {
		t.Fatalf("code = %q, want %q", cliErr.Code, output.CodeBadRequest)
	}
	if cliErr.Message != "invalid seek position: seconds must be < 60" {
		t.Fatalf("message = %q", cliErr.Message)
	}
}

func TestSeekRejectsMinutesGE60WithHours(t *testing.T) {
	_, cliErr := Seek(fakeClient("http://unused"), "1:99:30", true)
	if cliErr == nil {
		t.Fatal("want a CLIError")
	}
	if cliErr.Code != output.CodeBadRequest {
		t.Fatalf("code = %q, want %q", cliErr.Code, output.CodeBadRequest)
	}
	if cliErr.Message != "invalid seek position: minutes must be < 60 when hours given" {
		t.Fatalf("message = %q", cliErr.Message)
	}
}

func TestSeekAcceptsHighMinutesWithoutHours(t *testing.T) {
	// mm:ss with mm >= 60 stays valid -- only enforced when hours are given.
	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, jsonx.J{"state": "playing", "viewOffset": 0}, "server-mid")
	testutil.Setup(t, pms.URL)

	result, cliErr := Seek(fakeClient(csrv.URL), "99:30", true)
	if cliErr != nil {
		t.Fatalf("want no error, got %#v", cliErr)
	}
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	if result["playState"] != "playing" {
		t.Fatalf("playState = %v, want playing", result["playState"])
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

	result, cliErr := Seek(fakeClient(csrv.URL), "1:30", true)
	if cliErr != nil {
		t.Fatalf("want no error, got %#v", cliErr)
	}
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	if result["playState"] != "playing" {
		t.Fatalf("playState = %v, want playing", result["playState"])
	}
	seekOffset(t, fc, "90000")
}

func TestSeekAbsoluteHHMMSS(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, jsonx.J{"state": "playing", "viewOffset": 0}, "server-mid")
	testutil.Setup(t, pms.URL)

	result, cliErr := Seek(fakeClient(csrv.URL), "1:00:00", true)
	if cliErr != nil {
		t.Fatalf("want no error, got %#v", cliErr)
	}
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	seekOffset(t, fc, "3600000")
}

func TestSeekRelativePlusSeconds(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, jsonx.J{"state": "playing", "viewOffset": 60000}, "server-mid")
	testutil.Setup(t, pms.URL)

	result, cliErr := Seek(fakeClient(csrv.URL), "+30s", true)
	if cliErr != nil {
		t.Fatalf("want no error, got %#v", cliErr)
	}
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	seekOffset(t, fc, "90000")
}

func TestSeekRelativeMinusMinuteClampsToZero(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, jsonx.J{"state": "playing", "viewOffset": 60000}, "server-mid")
	testutil.Setup(t, pms.URL)

	result, cliErr := Seek(fakeClient(csrv.URL), "-1m", true)
	if cliErr != nil {
		t.Fatalf("want no error, got %#v", cliErr)
	}
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	seekOffset(t, fc, "0")
}

func TestSeekRelativeNoSession(t *testing.T) {
	_, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, nil, "server-mid") // no matching session
	testutil.Setup(t, pms.URL)

	_, cliErr := Seek(fakeClient(csrv.URL), "+30s", true)
	if cliErr == nil {
		t.Fatal("want a CLIError")
	}
	if cliErr.Code != output.CodeNothingPlaying {
		t.Fatalf("code = %q, want %q", cliErr.Code, output.CodeNothingPlaying)
	}
	if cliErr.Message != "could not determine current playback position" {
		t.Fatalf("message = %q", cliErr.Message)
	}
	if cliErr.Hint != "nothing to seek — start playback first" {
		t.Fatalf("hint = %q", cliErr.Hint)
	}
}

func TestSeekPausedDanceOrdering(t *testing.T) {
	oldSleep := sleep
	sleep = func(time.Duration) {}
	t.Cleanup(func() { sleep = oldSleep })

	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, jsonx.J{"state": "paused", "viewOffset": 0}, "server-mid")
	testutil.Setup(t, pms.URL)

	result, cliErr := Seek(fakeClient(csrv.URL), "1:30", true)
	if cliErr != nil {
		t.Fatalf("want no error, got %#v", cliErr)
	}
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	if result["playState"] != "paused" {
		t.Fatalf("playState = %v, want paused", result["playState"])
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
	// a session, so playState has no observed state to report and falls
	// back to the documented default ("playing").
	testutil.Setup(t, "http://pms.test:32400")

	result, cliErr := Seek(fakeClient(csrv.URL), "1:30", false)
	if cliErr != nil {
		t.Fatalf("want no error, got %#v", cliErr)
	}
	if !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v", result)
	}
	if result["playState"] != "playing" {
		t.Fatalf("playState = %v, want playing (default — no session read)", result["playState"])
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

	_, cliErr := Seek(fakeClient(csrv.URL), "1:30", true)
	if cliErr == nil {
		t.Fatal("want a CLIError")
	}
	if cliErr.Code != output.CodeSeekFailed {
		t.Fatalf("code = %q, want %q", cliErr.Code, output.CodeSeekFailed)
	}
	if !strings.HasPrefix(cliErr.Message, "could not resume before seek: ") {
		t.Fatalf("message = %q", cliErr.Message)
	}
	if cliErr.Hint != "try again" {
		t.Fatalf("hint = %q, want %q", cliErr.Hint, "try again")
	}
	if got := cliErr.Data["seeked"]; got != false {
		t.Fatalf("data.seeked = %v, want false", got)
	}
	if got := cliErr.Data["repaused"]; got != false {
		t.Fatalf("data.repaused = %v, want false", got)
	}
	for _, p := range fc.paths() {
		if strings.HasSuffix(p, "/seekTo") {
			t.Fatal("seekTo must not be called after a resume failure")
		}
	}
}

// TestSeekMidSequenceFailure covers the seekTo call itself failing (not the
// resume, not the re-pause): CodeSeekFailed with neither leg completed.
func TestSeekMidSequenceFailure(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	fc.failPath("/player/playback/seekTo", 500)
	pms := newFakePMS(t, jsonx.J{"state": "playing", "viewOffset": 0}, "server-mid")
	testutil.Setup(t, pms.URL)

	_, cliErr := Seek(fakeClient(csrv.URL), "1:30", true)
	if cliErr == nil {
		t.Fatal("want a CLIError")
	}
	if cliErr.Code != output.CodeSeekFailed {
		t.Fatalf("code = %q, want %q", cliErr.Code, output.CodeSeekFailed)
	}
	if cliErr.Hint != "try again" {
		t.Fatalf("hint = %q, want %q", cliErr.Hint, "try again")
	}
	if got := cliErr.Data["seeked"]; got != false {
		t.Fatalf("data.seeked = %v, want false", got)
	}
	if got := cliErr.Data["repaused"]; got != false {
		t.Fatalf("data.repaused = %v, want false", got)
	}
	_ = fc
}

func TestSeekRepauseFailureMessage(t *testing.T) {
	oldSleep := sleep
	sleep = func(time.Duration) {}
	t.Cleanup(func() { sleep = oldSleep })

	fc, csrv := newFakeCompanion(t)
	fc.failPath("/player/playback/pause", 500)
	pms := newFakePMS(t, jsonx.J{"state": "paused", "viewOffset": 0}, "server-mid")
	testutil.Setup(t, pms.URL)

	_, cliErr := Seek(fakeClient(csrv.URL), "1:30", true)
	if cliErr == nil {
		t.Fatal("want a CLIError")
	}
	if cliErr.Code != output.CodeSeekFailed {
		t.Fatalf("code = %q, want %q", cliErr.Code, output.CodeSeekFailed)
	}
	if !strings.HasPrefix(cliErr.Message, "seeked but failed to restore pause state: ") {
		t.Fatalf("message = %q", cliErr.Message)
	}
	// The seek itself landed -- the hint would wrongly suggest retrying the
	// whole sequence, so v2 leaves it empty here (contract-mandated
	// exception to CodeSeekFailed's default "try again" hint).
	if cliErr.Hint != "" {
		t.Fatalf("hint = %q, want empty", cliErr.Hint)
	}
	if got := cliErr.Data["seeked"]; got != true {
		t.Fatalf("data.seeked = %v, want true", got)
	}
	if got := cliErr.Data["repaused"]; got != false {
		t.Fatalf("data.repaused = %v, want false", got)
	}
	_ = fc
}

// --- PlayMedia / PlayQueue -------------------------------------------------

func TestPlayMediaAddressPortAndKeyShape(t *testing.T) {
	fc, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, nil, "server-mid")
	testutil.Setup(t, pms.URL)

	result, cliErr := PlayMedia(fakeClient(csrv.URL), "12345")
	if cliErr != nil || !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v (err %v)", result, cliErr)
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

	result, cliErr := PlayQueue(fakeClient(csrv.URL), "5535", "42687")
	if cliErr != nil || !jsonx.Truthy(result["ok"]) {
		t.Fatalf("want ok, got %#v (err %v)", result, cliErr)
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

	_, cliErr := PlayQueue(fakeClient(csrv.URL), "5535", "42687")
	if cliErr == nil || cliErr.Code != output.CodeInternal || cliErr.Message != "could not retrieve server machineIdentifier" {
		t.Fatalf("cliErr = %#v", cliErr)
	}
}

func TestPlayMediaMissingServerMachineID(t *testing.T) {
	_, csrv := newFakeCompanion(t)
	pms := newFakePMS(t, nil, "")
	testutil.Setup(t, pms.URL)

	_, cliErr := PlayMedia(fakeClient(csrv.URL), "12345")
	if cliErr == nil || cliErr.Code != output.CodeInternal || cliErr.Message != "could not retrieve server machineIdentifier" {
		t.Fatalf("cliErr = %#v", cliErr)
	}
}

// TestPlayRefusesRedirect pins W1 (finding 1): a Companion baseurl that 302s
// must never let X-Plex-Token reach the redirect target, and the resulting
// error must classify as connection-failed with no query string leaked.
func TestPlayRefusesRedirect(t *testing.T) {
	var targetHit bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	testutil.Setup(t, "http://pms.test:32400")

	_, cliErr := Play(fakeClient(srv.URL))
	if cliErr == nil {
		t.Fatal("want a CLIError")
	}
	if targetHit {
		t.Fatal("redirect target received a request — CheckRedirect did not fire before the request")
	}
	if cliErr.Code != output.CodeClientUnreachable {
		t.Fatalf("code = %q, want %q", cliErr.Code, output.CodeClientUnreachable)
	}
	if !strings.HasPrefix(cliErr.Message, "connection failed:") {
		t.Fatalf("want 'connection failed:' prefix, got %q", cliErr.Message)
	}
	if !strings.Contains(cliErr.Message, "redirect refused") {
		t.Fatalf("want 'redirect refused' in message, got %q", cliErr.Message)
	}
}

// TestCommandIDFileModesArePrivate pins W3 (finding 6): the counter's
// directory, lock file, and value file must never be group/world-readable.
// The config dir does not exist beforehand — t.TempDir() itself is created
// at 0755, so proving "MkdirAll now creates it private" requires a nested
// path that MkdirAll actually creates, not one that pre-exists. A
// pre-existing 0644 counter file self-heals to 0600 on its next write —
// temp+rename replaces the inode, so the new temp file's mode IS the final
// mode.
func TestCommandIDFileModesArePrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	t.Setenv("PLEXCTL_CONFIG_DIR", dir)
	t.Cleanup(resetCommandIDState)
	resetCommandIDState()

	nextCommandID()

	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("config dir mode = %o, err=%v, want 0700", info.Mode().Perm(), err)
	}
	commandIDFile := filepath.Join(dir, "commandid")
	if info, err := os.Stat(commandIDFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("commandid mode = %o, err=%v, want 0600", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(filepath.Join(dir, "commandid.lock")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("commandid.lock mode = %o, err=%v, want 0600", info.Mode().Perm(), err)
	}

	if err := os.Chmod(commandIDFile, 0o644); err != nil {
		t.Fatal(err)
	}
	nextCommandID()
	if info, err := os.Stat(commandIDFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("commandid mode after self-heal = %o, err=%v, want 0600", info.Mode().Perm(), err)
	}
}
