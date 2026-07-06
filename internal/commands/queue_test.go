package commands_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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
	// No "/" route registered -> GetServerMachineID fails inside
	// queue.Create, which must short-circuit before ever resolving a client.
	f := newFakePMS(t)
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
	_ = f
}
