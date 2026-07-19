package commands_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/queue"
	"github.com/corinthian/plexctl/internal/testutil"
)

// fastVerify compresses ConfirmEngaged's polling window so wedge-shaped
// tests (bind accepted, client never engages) don't sit through the real
// 12-probe sleep schedule.
func fastVerify(t *testing.T) {
	t.Helper()
	oldProbes, oldSleep := queue.VerifyProbes, queue.VerifySleep
	queue.VerifyProbes = 2
	queue.VerifySleep = func(time.Duration) {}
	t.Cleanup(func() { queue.VerifyProbes, queue.VerifySleep = oldProbes, oldSleep })
}

// stagedEntry reads the persisted queue-state entry for the fake's default
// Apple TV client (mid-appletv), asserting it was written.
func stagedEntry(t *testing.T, f *fakePMS) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "queue_state.json"))
	if err != nil {
		t.Fatalf("reading queue_state.json: %v", err)
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("bad queue_state.json: %v (%s)", err, raw)
	}
	entry, ok := state["mid-appletv"].(map[string]any)
	if !ok {
		t.Fatalf("no staged entry for mid-appletv: %#v", state)
	}
	return entry
}

// errEnvelope pulls the v2 {"error":{"code",...},"data":{...}} shape out of
// an unmarshaled CLI response, failing the test if "error" isn't present.
func errEnvelope(t *testing.T, got map[string]any) (code string, data map[string]any) {
	t.Helper()
	errObj, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error object in %#v", got)
	}
	code, _ = errObj["code"].(string)
	data, _ = got["data"].(map[string]any)
	return code, data
}

func TestQueueSavesQueueState(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onJSON("POST", "/playQueues", map[string]any{
		"MediaContainer": map[string]any{"playQueueID": "555", "playQueueSelectedItemID": "999"},
	})
	f.onStatus("GET", "/player/playback/playMedia", 200)
	f.onSessions("playing", "123")

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue", "123"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1 (successful queue+play, no exit); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != true {
		t.Fatalf("ok = %#v, out=%s", got["ok"], out)
	}
	if got["clientEngaged"] != true {
		t.Fatalf("clientEngaged = %#v, want true; out=%s", got["clientEngaged"], out)
	}

	raw, err := os.ReadFile(filepath.Join(f.dir, "queue_state.json"))
	if err != nil {
		t.Fatalf("reading queue_state.json: %v", err)
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("bad queue_state.json: %v (%s)", err, raw)
	}
	entry, ok := state["mid-appletv"].(map[string]any)
	if !ok {
		t.Fatalf("no entry for mid-appletv in queue_state.json: %#v", state)
	}
	if entry["playQueueID"] != "555" || entry["selectedItemID"] != "999" {
		t.Fatalf("entry = %#v", entry)
	}
}

// TestQueueBindSuccessReadOnlyConfigDirReportsStateSavedFalse pins W10/D2:
// a state-write failure after a successful (bind + engagement) must not
// become a failure envelope — playback is already running. v2: the envelope
// stays ok:true, exit 0, and carries a PLEX_STATE_SAVE_FAILED warning
// instead of the v1 bare stateSaved:false key.
func TestQueueBindSuccessReadOnlyConfigDirReportsStateSavedFalse(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onJSON("POST", "/playQueues", map[string]any{
		"MediaContainer": map[string]any{"playQueueID": "555", "playQueueSelectedItemID": "999"},
	})
	f.onStatus("GET", "/player/playback/playMedia", 200)
	f.onSessions("playing", "123")

	if err := os.Chmod(f.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.dir, 0o700) })

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue", "123"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1 (ok:true despite the write failure); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != true {
		t.Fatalf("ok = %#v, want true; out=%s", got["ok"], out)
	}
	if _, present := got["stateSaved"]; present {
		t.Fatalf("v1 bare stateSaved key must be gone: out=%s", out)
	}
	warnings, ok := got["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want exactly one; out=%s", got["warnings"], out)
	}
	w, _ := warnings[0].(map[string]any)
	if w["code"] != output.CodeStateSaveFailed {
		t.Fatalf("warning code = %#v, want %s", w["code"], output.CodeStateSaveFailed)
	}
	if got["playQueueID"] != "555" {
		t.Fatalf("playQueueID = %#v, want 555; out=%s", got["playQueueID"], out)
	}
}

// TestQueueBindFailureStagedNeverTrueWithoutPersistedEntry pins the other
// half of D2: on the SaveIfAbsent staging path, `staged` must never be
// true unless the write actually persisted. A dead client (bind failure)
// plus a read-only config dir means SaveIfAbsent's own write fails too. v2:
// this is a plexctl-side fault distinct from both STAGED and CONFLICT — it
// maps to CodeInternal (flagged in the P2-E report; not in the given closed
// mapping).
func TestQueueBindFailureStagedNeverTrueWithoutPersistedEntry(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onJSON("POST", "/playQueues", map[string]any{
		"MediaContainer": map[string]any{"playQueueID": "555", "playQueueSelectedItemID": "999"},
	})
	f.onStatus("GET", "/player/playback/playMedia", 404) // bind fails: HTTP error, not transport

	if err := os.Chmod(f.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.dir, 0o700) })

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue", "123"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != output.ExitInternal {
		t.Fatalf("exit = %d, want %d (bind + stage both failed); out=%s", code, output.ExitInternal, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false; out=%s", got["ok"], out)
	}
	errCode, data := errEnvelope(t, got)
	if errCode != output.CodeInternal {
		t.Fatalf("code = %q, want %s", errCode, output.CodeInternal)
	}
	if _, present := data["staged"]; present {
		t.Fatalf("staged present with no persisted entry (write was blocked): %#v", data)
	}
	errObj, _ := got["error"].(map[string]any)
	msg, _ := errObj["message"].(string)
	if !containsAll(msg, "additionally failed to stage queue state") {
		t.Fatalf("message = %q, want it to mention the staging failure too", msg)
	}
}

// containsAll is a tiny substring-presence helper for message-content spot
// checks (message text is documented as unstable/never-match-on, so this is
// used sparingly, only where the mapping calls for a specific detail).
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// C1 (finding 2): with the target client registered but inactive, `queue`
// must resolve-and-exit BEFORE creating any playQueue — zero POSTs to
// /playQueues — so a bind-impossible client never orphans server-side state.
// v2: clients.Resolve now emits the coded PLEX_CLIENT_INACTIVE envelope
// (exit 2) instead of the v1 flat "not active" string at exit 1 (P2-B,
// commit a1d4a49).
func TestQueueResolvesBeforeCreateNoOrphanOnInactiveClient(t *testing.T) {
	f := newFakePMS(t)
	f.inactiveClient(t)
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onJSON("POST", "/playQueues", map[string]any{
		"MediaContainer": map[string]any{"playQueueID": "555", "playQueueSelectedItemID": "999"},
	})

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue", "123"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })

	if got := f.countMethod("POST"); got != 0 {
		t.Fatalf("POST count = %d, want 0 (Create must not run for an inactive client); out=%s", got, out)
	}
	if code != output.ExitPlex {
		t.Fatalf("exit = %d, want %d (resolver print-and-exit); out=%s", code, output.ExitPlex, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false; out=%s", got["ok"], out)
	}
	errCode, _ := errEnvelope(t, got)
	if errCode != output.CodeClientInactive {
		t.Fatalf("code = %q, want %s; out=%s", errCode, output.CodeClientInactive, out)
	}
}

// B1: a transport-shaped bind failure (the device didn't answer) keeps the
// created queue, stages its state, and flags clientUnreachable + IDs (all in
// data) so the caller knows a queue exists to recover — not that nothing
// happened.
func TestQueueBindTimeoutStagesQueueWithClientUnreachable(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onJSON("POST", "/playQueues", map[string]any{
		"MediaContainer": map[string]any{"playQueueID": "555", "playQueueSelectedItemID": "999"},
	})
	// The Companion bind hangs past the timeout -> "request timed out:".
	f.on("GET", "/player/playback/playMedia", func(r *http.Request) (int, any) {
		time.Sleep(250 * time.Millisecond)
		return 200, nil
	})
	api.SetTimeoutOverride(0.05)
	t.Cleanup(api.ClearTimeoutOverride)

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue", "123"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != output.ExitPlex {
		t.Fatalf("exit = %d, want %d (CodeQueueStaged); out=%s", code, output.ExitPlex, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, out=%s", got["ok"], out)
	}
	errCode, data := errEnvelope(t, got)
	if errCode != output.CodeQueueStaged {
		t.Fatalf("code = %q, want %s", errCode, output.CodeQueueStaged)
	}
	if data["playQueueID"] != "555" || data["selectedItemID"] != "999" {
		t.Fatalf("bind-failure data must carry both IDs: %#v", data)
	}
	if data["clientUnreachable"] != true {
		t.Fatalf("clientUnreachable = %#v, want true", data["clientUnreachable"])
	}
	if data["staged"] != true {
		t.Fatalf("staged = %#v, want true", data["staged"])
	}
	if entry := stagedEntry(t, f); entry["playQueueID"] != "555" || entry["selectedItemID"] != "999" {
		t.Fatalf("staged entry = %#v", entry)
	}
}

// B1: an HTTP-error bind (reachable client, 5xx) still stages the queue and
// carries IDs, but clientUnreachable must be false (present, not absent —
// docs/error_model_v2.md §1's example envelope always carries the key).
func TestQueueBindHTTP500StagesQueueWithoutClientUnreachable(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onJSON("POST", "/playQueues", map[string]any{
		"MediaContainer": map[string]any{"playQueueID": "555", "playQueueSelectedItemID": "999"},
	})
	f.onStatus("GET", "/player/playback/playMedia", 500)

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue", "123"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != output.ExitPlex {
		t.Fatalf("exit = %d, want %d (CodeQueueStaged); out=%s", code, output.ExitPlex, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, out=%s", got["ok"], out)
	}
	errCode, data := errEnvelope(t, got)
	if errCode != output.CodeQueueStaged {
		t.Fatalf("code = %q, want %s", errCode, output.CodeQueueStaged)
	}
	if data["playQueueID"] != "555" || data["selectedItemID"] != "999" {
		t.Fatalf("bind-failure data must carry both IDs: %#v", data)
	}
	if data["clientUnreachable"] != false {
		t.Fatalf("clientUnreachable = %#v, want false (present, not absent) for an HTTP-error bind", data["clientUnreachable"])
	}
	if entry := stagedEntry(t, f); entry["playQueueID"] != "555" {
		t.Fatalf("staged entry = %#v", entry)
	}
}

// C2 (finding 1): a failed bind of a NEW queue must NOT clobber the client's
// existing (bound/playing) entry. SaveIfAbsent preserves it, the output
// carries the new queue's IDs (as orphanedQueueID) and the preserved one
// as activeQueueID — the v2 CodeQueueConflict shape, replacing v1's
// key-presence inference.
func TestQueueBindFailurePreservesExistingEntryNoStaged(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	seed := map[string]any{"mid-appletv": map[string]any{
		"playQueueID": "111", "selectedItemID": "222", "savedAt": float64(1),
	}}
	seedBytes, _ := json.Marshal(seed)
	if err := os.WriteFile(filepath.Join(f.dir, "queue_state.json"), seedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onJSON("POST", "/playQueues", map[string]any{
		"MediaContainer": map[string]any{"playQueueID": "555", "playQueueSelectedItemID": "999"},
	})
	f.on("GET", "/player/playback/playMedia", func(r *http.Request) (int, any) {
		time.Sleep(250 * time.Millisecond)
		return 200, nil
	})
	api.SetTimeoutOverride(0.05)
	t.Cleanup(api.ClearTimeoutOverride)

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue", "123"})
	out, _ := testutil.Capture(t, func() { _ = root.Execute() })
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, out=%s", got["ok"], out)
	}
	errCode, data := errEnvelope(t, got)
	if errCode != output.CodeQueueConflict {
		t.Fatalf("code = %q, want %s", errCode, output.CodeQueueConflict)
	}
	if _, present := data["staged"]; present {
		t.Fatalf("staged must be ABSENT on CONFLICT: %#v", data)
	}
	if data["orphanedQueueID"] != "555" {
		t.Fatalf("data must carry the new queue's ID as orphanedQueueID: %#v", data)
	}
	if data["activeQueueID"] != "111" {
		t.Fatalf("data must carry the preserved entry's ID as activeQueueID: %#v", data)
	}
	if entry := stagedEntry(t, f); entry["playQueueID"] != "111" || entry["selectedItemID"] != "222" {
		t.Fatalf("existing entry was clobbered: %#v", entry)
	}
}

// C2 (finding 1): with no prior entry, a failed bind stages the new queue and
// data.staged:true so queue-start knows it can recover it.
func TestQueueBindFailureStagesWhenNoEntryEmitsStagedKey(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onJSON("POST", "/playQueues", map[string]any{
		"MediaContainer": map[string]any{"playQueueID": "555", "playQueueSelectedItemID": "999"},
	})
	f.on("GET", "/player/playback/playMedia", func(r *http.Request) (int, any) {
		time.Sleep(250 * time.Millisecond)
		return 200, nil
	})
	api.SetTimeoutOverride(0.05)
	t.Cleanup(api.ClearTimeoutOverride)

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue", "123"})
	out, _ := testutil.Capture(t, func() { _ = root.Execute() })
	got := mustUnmarshal(t, out)
	errCode, data := errEnvelope(t, got)
	if errCode != output.CodeQueueStaged {
		t.Fatalf("code = %q, want %s", errCode, output.CodeQueueStaged)
	}
	if data["staged"] != true {
		t.Fatalf("staged must be true when a fresh queue was staged: %#v", data)
	}
	if entry := stagedEntry(t, f); entry["playQueueID"] != "555" {
		t.Fatalf("staged entry = %#v", entry)
	}
}

// C2: the bind-SUCCESS path is unchanged — it overwrites any existing entry
// with the live queue and never sets staged (only applies to a queue-scoped
// error envelope).
func TestQueueBindSuccessOverwritesExistingEntry(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	seed := map[string]any{"mid-appletv": map[string]any{
		"playQueueID": "111", "selectedItemID": "222", "savedAt": float64(1),
	}}
	seedBytes, _ := json.Marshal(seed)
	if err := os.WriteFile(filepath.Join(f.dir, "queue_state.json"), seedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onJSON("POST", "/playQueues", map[string]any{
		"MediaContainer": map[string]any{"playQueueID": "555", "playQueueSelectedItemID": "999"},
	})
	f.onStatus("GET", "/player/playback/playMedia", 200)
	f.onSessions("playing", "123")

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue", "123"})
	out, _ := testutil.Capture(t, func() { _ = root.Execute() })
	got := mustUnmarshal(t, out)
	if got["ok"] != true {
		t.Fatalf("ok = %#v, out=%s", got["ok"], out)
	}
	if _, present := got["staged"]; present {
		t.Fatalf("success path must never set staged: %#v", got)
	}
	if entry := stagedEntry(t, f); entry["playQueueID"] != "555" || entry["selectedItemID"] != "999" {
		t.Fatalf("success must overwrite existing entry with the new queue: %#v", entry)
	}
}

// B3: queue-start is registered and wired through the full resolver; with no
// staged queue it surfaces the coded PLEX_NO_QUEUE error at exit 2.
func TestQueueStartCommandNoStateReturnsNoActiveQueue(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue-start"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != output.ExitPlex {
		t.Fatalf("exit = %d, want %d; out=%s", code, output.ExitPlex, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("got %#v", got)
	}
	errCode, _ := errEnvelope(t, got)
	if errCode != output.CodeNoQueue {
		t.Fatalf("code = %q, want %s", errCode, output.CodeNoQueue)
	}
}

// Wedge (incidents 2026-07-05/13): the Companion listener 200s the bind but
// the app never engages — sessions stay empty. `queue` must not claim
// success; the new queue stages (no prior entry) and CodePlaybackNotStarted
// carries data.staged:true (recoverable via queue-start).
func TestQueueEngagementFailureStagesAndAdvisesQueueStart(t *testing.T) {
	f := newFakePMS(t)
	fastVerify(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onJSON("POST", "/playQueues", map[string]any{
		"MediaContainer": map[string]any{"playQueueID": "555", "playQueueSelectedItemID": "999"},
	})
	f.onStatus("GET", "/player/playback/playMedia", 200)
	f.onSessionsIdle()

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue", "123"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != output.ExitPlex {
		t.Fatalf("exit = %d, want %d (engagement failure); out=%s", code, output.ExitPlex, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false; out=%s", got["ok"], out)
	}
	errCode, data := errEnvelope(t, got)
	if errCode != output.CodePlaybackNotStarted {
		t.Fatalf("code = %q, want %s", errCode, output.CodePlaybackNotStarted)
	}
	if data["clientEngaged"] != false {
		t.Fatalf("clientEngaged = %#v, want false; out=%s", data["clientEngaged"], out)
	}
	if data["staged"] != true {
		t.Fatalf("staged = %#v, want true (fresh queue, no prior entry); out=%s", data["staged"], out)
	}
	if entry := stagedEntry(t, f); entry["playQueueID"] != "555" {
		t.Fatalf("staged entry = %#v", entry)
	}
}

// A session playing something ELSE on the client must not count as
// engagement (stale-session hazard). With an existing persisted entry, the
// engagement failure follows the SAME staging decision as a bind failure
// (queue.Stage): the new queue is NOT staged, data.staged:false, and the
// existing entry is preserved untouched.
func TestQueueEngagementFailurePreservesEntryAdvisesRequeue(t *testing.T) {
	f := newFakePMS(t)
	fastVerify(t)
	f.resolvableClient(t)
	seed := map[string]any{"mid-appletv": map[string]any{
		"playQueueID": "111", "selectedItemID": "222", "savedAt": float64(1),
	}}
	seedBytes, _ := json.Marshal(seed)
	if err := os.WriteFile(filepath.Join(f.dir, "queue_state.json"), seedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onJSON("POST", "/playQueues", map[string]any{
		"MediaContainer": map[string]any{"playQueueID": "555", "playQueueSelectedItemID": "999"},
	})
	f.onStatus("GET", "/player/playback/playMedia", 200)
	f.onSessions("playing", "888") // old content, not the queued key 123

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue", "123"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != output.ExitPlex {
		t.Fatalf("exit = %d, want %d; out=%s", code, output.ExitPlex, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false; out=%s", got["ok"], out)
	}
	errCode, data := errEnvelope(t, got)
	if errCode != output.CodePlaybackNotStarted {
		t.Fatalf("code = %q, want %s", errCode, output.CodePlaybackNotStarted)
	}
	if data["clientEngaged"] != false {
		t.Fatalf("clientEngaged = %#v, want false; out=%s", data["clientEngaged"], out)
	}
	if data["staged"] != false {
		t.Fatalf("staged = %#v, want false when an existing entry was preserved: out=%s", data["staged"], out)
	}
	if entry := stagedEntry(t, f); entry["playQueueID"] != "111" {
		t.Fatalf("existing entry was clobbered: %#v", entry)
	}
}

// queue-start has the identical accepted-vs-engaged gap. On a wedge it must
// flip to CodePlaybackNotStarted; the persisted entry stays untouched for
// that retry, so data.staged is unconditionally true.
func TestQueueStartEngagementFailure(t *testing.T) {
	f := newFakePMS(t)
	fastVerify(t)
	f.resolvableClient(t)
	seed := map[string]any{"mid-appletv": map[string]any{
		"playQueueID": "111", "selectedItemID": "222", "savedAt": float64(1),
	}}
	seedBytes, _ := json.Marshal(seed)
	if err := os.WriteFile(filepath.Join(f.dir, "queue_state.json"), seedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onStatus("GET", "/player/playback/playMedia", 200)
	f.onJSON("GET", "/playQueues/111", map[string]any{
		"MediaContainer": map[string]any{"Metadata": []any{map[string]any{"ratingKey": "777"}}},
	})
	f.onSessionsIdle()

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue-start"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != output.ExitPlex {
		t.Fatalf("exit = %d, want %d; out=%s", code, output.ExitPlex, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false; out=%s", got["ok"], out)
	}
	errCode, data := errEnvelope(t, got)
	if errCode != output.CodePlaybackNotStarted {
		t.Fatalf("code = %q, want %s", errCode, output.CodePlaybackNotStarted)
	}
	if data["clientEngaged"] != false {
		t.Fatalf("clientEngaged = %#v, want false; out=%s", data["clientEngaged"], out)
	}
	if data["staged"] != true {
		t.Fatalf("staged = %#v, want true (queue-start never un-saves state); out=%s", data["staged"], out)
	}
	if entry := stagedEntry(t, f); entry["playQueueID"] != "111" {
		t.Fatalf("entry must survive for the retry: %#v", entry)
	}
}

// queue-start success: engagement is scoped to the queue's own items
// (fetched from PMS), and a verified bind carries clientEngaged:true.
func TestQueueStartEngagedSuccess(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	seed := map[string]any{"mid-appletv": map[string]any{
		"playQueueID": "111", "selectedItemID": "222", "savedAt": float64(1),
	}}
	seedBytes, _ := json.Marshal(seed)
	if err := os.WriteFile(filepath.Join(f.dir, "queue_state.json"), seedBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onStatus("GET", "/player/playback/playMedia", 200)
	f.onJSON("GET", "/playQueues/111", map[string]any{
		"MediaContainer": map[string]any{"Metadata": []any{map[string]any{"ratingKey": "777"}}},
	})
	f.onSessions("playing", "777")

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue-start"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != true || got["clientEngaged"] != true {
		t.Fatalf("ok/clientEngaged = %#v/%#v, want true/true; out=%s", got["ok"], got["clientEngaged"], out)
	}
}

// TestQueuePassesThroughCreateFailure pins the v2 mapping: "could not
// retrieve server machineIdentifier" is enumerated verbatim under
// CodeInternal (exit 4) in the error-model-v2 code table.
func TestQueuePassesThroughCreateFailure(t *testing.T) {
	// C1 reordered Resolve ahead of Create, so the client must resolve first;
	// with no "/" route GetServerMachineID then fails inside queue.Create and
	// that failure passes through to the output (exit 4, CodeInternal).
	// (Pre-C1 this test wired no client because Create ran first — the
	// resolve-before-create contract now requires a resolvable client for
	// Create to be reached at all.)
	f := newFakePMS(t)
	f.resolvableClient(t)
	root := commands.BuildRoot()
	root.SetArgs([]string{"queue", "123"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != output.ExitInternal {
		t.Fatalf("exit = %d, want %d; out=%s", code, output.ExitInternal, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("got %#v", got)
	}
	errCode, _ := errEnvelope(t, got)
	if errCode != output.CodeInternal {
		t.Fatalf("code = %q, want %s", errCode, output.CodeInternal)
	}
	errObj, _ := got["error"].(map[string]any)
	if errObj["message"] != "could not retrieve server machineIdentifier" {
		t.Fatalf("message = %#v", errObj["message"])
	}
}

// TestQueueShuffleCommandRefusesWithoutHTTP and
// TestQueueUnshuffleCommandRefusesWithoutHTTP pin the v2 behavior change
// end-to-end through the real CLI path: PMS 1.43 404s both endpoints, so
// plexctl now refuses immediately — no client resolution (which itself
// would dial PMS + plex.tv), no HTTP call of any kind — instead of
// forwarding a misleading 404 (docs/error_model_v2.md §3).
func TestQueueShuffleCommandRefusesWithoutHTTP(t *testing.T) {
	f := newFakePMS(t)
	// Deliberately no resolvableClient()/inactiveClient() wiring: any HTTP
	// call this command made would hit an unregistered route and fail loudly.

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue-shuffle"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != output.ExitPlex {
		t.Fatalf("exit = %d, want %d; out=%s", code, output.ExitPlex, out)
	}
	got := mustUnmarshal(t, out)
	errCode, _ := errEnvelope(t, got)
	if errCode != output.CodeUnsupported {
		t.Fatalf("code = %q, want %s; out=%s", errCode, output.CodeUnsupported, out)
	}
	if len(f.calls) != 0 {
		t.Fatalf("expected no network calls at all, got %#v", f.calls)
	}
}

func TestQueueUnshuffleCommandRefusesWithoutHTTP(t *testing.T) {
	f := newFakePMS(t)

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue-unshuffle"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != output.ExitPlex {
		t.Fatalf("exit = %d, want %d; out=%s", code, output.ExitPlex, out)
	}
	got := mustUnmarshal(t, out)
	errCode, _ := errEnvelope(t, got)
	if errCode != output.CodeUnsupported {
		t.Fatalf("code = %q, want %s; out=%s", errCode, output.CodeUnsupported, out)
	}
	if len(f.calls) != 0 {
		t.Fatalf("expected no network calls at all, got %#v", f.calls)
	}
}
