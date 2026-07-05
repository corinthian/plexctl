package commands_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/testutil"
)

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
