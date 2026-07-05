package queuestate_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/queuestate"
)

func setup(t *testing.T) string {
	dir := t.TempDir()
	t.Setenv("PLEXCTL_CONFIG_DIR", dir)
	return dir
}

func TestSaveLoadClearRoundTrip(t *testing.T) {
	setup(t)
	queuestate.Save("mid-1", "5535", "42687")
	entry := queuestate.Load("mid-1")
	if entry == nil {
		t.Fatal("Load returned nil after Save")
	}
	if entry["playQueueID"] != "5535" || entry["selectedItemID"] != "42687" {
		t.Fatalf("round trip: %#v", entry)
	}
	if !jsonx.Truthy(entry["savedAt"]) {
		t.Fatal("savedAt missing")
	}
	queuestate.Clear("mid-1")
	if queuestate.Load("mid-1") != nil {
		t.Fatal("Clear did not remove entry")
	}
}

func TestPythonFileFormatCompatible(t *testing.T) {
	// The Go port must read a state file the Python binary wrote, and write
	// one the Python binary can read (cutover keeps the live file).
	dir := setup(t)
	py := `{
  "mid-py": {
    "playQueueID": "777",
    "selectedItemID": null,
    "savedAt": 1762290000
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "queue_state.json"), []byte(py), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := queuestate.Load("mid-py")
	if entry == nil || entry["playQueueID"] != "777" || entry["selectedItemID"] != nil {
		t.Fatalf("python-written state misread: %#v", entry)
	}
	queuestate.Save("mid-go", "888", "")
	b, err := os.ReadFile(filepath.Join(dir, "queue_state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]jsonx.J
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatalf("state file not valid JSON: %v", err)
	}
	if state["mid-py"]["playQueueID"] != "777" {
		t.Fatal("existing entries clobbered")
	}
	if v, present := state["mid-go"]["selectedItemID"]; !present || v != nil {
		t.Fatalf("empty selectedID should serialize as null, got %#v (present=%v)", v, present)
	}
}

func TestEmptyArgsAreNoOps(t *testing.T) {
	setup(t)
	queuestate.Save("", "5", "1")
	queuestate.Save("m", "", "1")
	if queuestate.Load("") != nil || queuestate.Load("m") != nil {
		t.Fatal("empty-arg save should be a no-op")
	}
	queuestate.Clear("") // must not panic
}
