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
	"github.com/corinthian/plexctl/internal/testutil"
)

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

func TestQueueSavesQueueState(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onJSON("POST", "/playQueues", map[string]any{
		"MediaContainer": map[string]any{"playQueueID": "555", "playQueueSelectedItemID": "999"},
	})
	f.onStatus("GET", "/player/playback/playMedia", 200)

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

// C1 (finding 2): with the target client registered but inactive, `queue`
// must resolve-and-exit BEFORE creating any playQueue — zero POSTs to
// /playQueues — so a bind-impossible client never orphans server-side state.
// Pre-C1 Create ran first and the queue leaked with no IDs in the output.
// The create + server-id routes are wired so a regression (Create wrongly
// reached) would make the POST succeed and the callCount assertion fail loudly.
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
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (resolver print-and-exit); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false; out=%s", got["ok"], out)
	}
	if errStr, _ := got["error"].(string); !strings.Contains(errStr, "not active") {
		t.Fatalf("error = %q, want the resolver 'registered but not active' message", got["error"])
	}
}

// B1: a transport-shaped bind failure (the device didn't answer) keeps the
// created queue, stages its state, and flags clientUnreachable + IDs so the
// caller knows a queue exists to recover — not that nothing happened.
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
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (timeout prefix); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, out=%s", got["ok"], out)
	}
	if got["playQueueID"] != "555" || got["selectedItemID"] != "999" {
		t.Fatalf("bind-failure result must carry both IDs: %#v", got)
	}
	if got["clientUnreachable"] != true {
		t.Fatalf("clientUnreachable = %#v, want true", got["clientUnreachable"])
	}
	if entry := stagedEntry(t, f); entry["playQueueID"] != "555" || entry["selectedItemID"] != "999" {
		t.Fatalf("staged entry = %#v", entry)
	}
}

// B1: an HTTP-error bind (reachable client, 5xx) still stages the queue and
// carries IDs, but must NOT claim the client is unreachable.
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
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (HTTP error); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, out=%s", got["ok"], out)
	}
	if got["playQueueID"] != "555" || got["selectedItemID"] != "999" {
		t.Fatalf("bind-failure result must carry both IDs: %#v", got)
	}
	if _, present := got["clientUnreachable"]; present {
		t.Fatalf("clientUnreachable must be absent for an HTTP-error bind: %#v", got)
	}
	if entry := stagedEntry(t, f); entry["playQueueID"] != "555" {
		t.Fatalf("staged entry = %#v", entry)
	}
}

// C2 (finding 1): a failed bind of a NEW queue must NOT clobber the client's
// existing (bound/playing) entry. SaveIfAbsent preserves it, the output carries
// the new queue's IDs but NO staged key (recovery is re-running `queue`).
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
	if _, present := got["staged"]; present {
		t.Fatalf("staged must be ABSENT when an existing entry was preserved: %#v", got)
	}
	if got["playQueueID"] != "555" {
		t.Fatalf("output must still carry the new queue's IDs: %#v", got)
	}
	if entry := stagedEntry(t, f); entry["playQueueID"] != "111" || entry["selectedItemID"] != "222" {
		t.Fatalf("existing entry was clobbered: %#v", entry)
	}
}

// C2 (finding 1): with no prior entry, a failed bind stages the new queue and
// the output carries staged:true so queue-start knows it can recover it.
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
	if got["staged"] != true {
		t.Fatalf("staged must be true when a fresh queue was staged: %#v", got)
	}
	if entry := stagedEntry(t, f); entry["playQueueID"] != "555" {
		t.Fatalf("staged entry = %#v", entry)
	}
}

// C2: the bind-SUCCESS path is unchanged — it overwrites any existing entry
// with the live queue and never sets staged.
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
// staged queue it surfaces the no-active-queue error the skill translates.
func TestQueueStartCommandNoStateReturnsNoActiveQueue(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)

	root := commands.BuildRoot()
	root.SetArgs([]string{"queue-start"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false || got["error"] != "no active queue on Apple TV" {
		t.Fatalf("got %#v", got)
	}
}

func TestQueuePassesThroughCreateFailure(t *testing.T) {
	// C1 reordered Resolve ahead of Create, so the client must resolve first;
	// with no "/" route GetServerMachineID then fails inside queue.Create and
	// that failure passes through to the output (exit 1). (Pre-C1 this test
	// wired no client because Create ran first — the resolve-before-create
	// contract now requires a resolvable client for Create to be reached at all.)
	f := newFakePMS(t)
	f.resolvableClient(t)
	root := commands.BuildRoot()
	root.SetArgs([]string{"queue", "123"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false || got["error"] != "could not retrieve server machineIdentifier" {
		t.Fatalf("got %#v", got)
	}
}
