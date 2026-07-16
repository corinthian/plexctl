package queuestate_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

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

// TestWriteFailuresPropagateInsteadOfSilentlyDiscarding pins W10: writeAll
// used to discard MkdirAll/Marshal/WriteFile/Rename errors entirely. Save,
// SaveIfAbsent, and Clear now all return the underlying error, and
// SaveIfAbsent's wrote bool is only ever true when the write itself
// succeeded (the :117 invariant comment made true, not just aspirational).
func TestWriteFailuresPropagateInsteadOfSilentlyDiscarding(t *testing.T) {
	dir := setup(t)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := queuestate.Save("mid-1", "q1", "s1"); err == nil {
		t.Fatal("Save on a read-only dir returned nil error")
	}
	wrote, err := queuestate.SaveIfAbsent("mid-1", "q1", "s1")
	if err == nil {
		t.Fatal("SaveIfAbsent on a read-only dir returned nil error")
	}
	if wrote {
		t.Fatal("SaveIfAbsent reported wrote=true despite the write failing")
	}
}

// TestWriteAllRemovesTmpFileOnRenameFailure pins W13: writeAll's rename
// step left a stale queue_state.json.tmp behind on failure — a fixed
// filename that would silently mask the next write's own temp file
// creation racing with leftover cruft. Force the rename to fail by
// pre-creating a non-empty directory at the destination path (WriteFile
// on the .tmp path still succeeds; Rename onto a non-empty directory
// does not).
func TestWriteAllRemovesTmpFileOnRenameFailure(t *testing.T) {
	dir := setup(t)
	statePath := filepath.Join(dir, "queue_state.json")
	if err := os.Mkdir(statePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(statePath, "occupied"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := queuestate.Save("mid-1", "q1", "s1")
	if err == nil {
		t.Fatal("expected a rename error, got nil")
	}
	if _, statErr := os.Stat(statePath + ".tmp"); !os.IsNotExist(statErr) {
		t.Fatalf("leftover .tmp file after failed rename: statErr=%v", statErr)
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

// C5 (finding 6): concurrent read-modify-writes on distinct mids must not lose
// an update. Under the flock this is deterministic; without it the interleaved
// readAll→writeAll windows drop entries. All goroutines fire from a barrier to
// maximize overlap — the test passes reliably locked and would fail unlocked.
func TestConcurrentSavesNoLostUpdate(t *testing.T) {
	setup(t)
	const n = 64
	var start, done sync.WaitGroup
	start.Add(1)
	for i := 0; i < n; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			queuestate.Save(fmt.Sprintf("mid-%d", i), fmt.Sprintf("q%d", i), "s")
		}(i)
	}
	start.Done()
	done.Wait()

	for i := 0; i < n; i++ {
		if queuestate.Load(fmt.Sprintf("mid-%d", i)) == nil {
			t.Fatalf("lost update: mid-%d absent after %d concurrent Saves", i, n)
		}
	}
}

// C5 (finding 6): Save genuinely serializes on queue_state.lock. The test holds
// the flock, confirms Save blocks (does not complete or degrade past a held
// lock), then releases and confirms Save proceeds and persists. This proves the
// lock is real, not a no-op that would let the lost-update race back in.
func TestSaveSerializesUnderHeldLock(t *testing.T) {
	dir := setup(t)
	lf, err := os.OpenFile(filepath.Join(dir, "queue_state.lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		queuestate.Save("mid-1", "q1", "s1")
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Save completed while the lock was held — lock is not serializing")
	case <-time.After(150 * time.Millisecond):
		// expected: Save is blocked waiting on the lock
	}

	_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	_ = lf.Close()

	select {
	case <-done:
		// Save proceeded once the lock freed
	case <-time.After(2 * time.Second):
		t.Fatal("Save did not complete after lock release")
	}
	if entry := queuestate.Load("mid-1"); entry == nil || entry["playQueueID"] != "q1" {
		t.Fatalf("Save did not persist after lock release: %#v", entry)
	}
}

// TestSaveFileModesArePrivate pins W3 (finding 6): the state directory, its
// lock file, and the state file itself must never be group/world-readable.
// The config dir does not exist beforehand — t.TempDir() itself is created
// at 0755, so proving "MkdirAll now creates it private" requires a nested
// path that MkdirAll actually creates, not one that pre-exists. A
// pre-existing 0644 state file self-heals to 0600 on its next write —
// temp+rename replaces the inode, so the new temp file's mode IS the final
// mode.
func TestSaveFileModesArePrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg")
	t.Setenv("PLEXCTL_CONFIG_DIR", dir)

	if err := queuestate.Save("mid-1", "q1", "s1"); err != nil {
		t.Fatal(err)
	}

	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("config dir mode = %o, err=%v, want 0700", info.Mode().Perm(), err)
	}
	statePath := filepath.Join(dir, "queue_state.json")
	if info, err := os.Stat(statePath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("queue_state.json mode = %o, err=%v, want 0600", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(filepath.Join(dir, "queue_state.lock")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("queue_state.lock mode = %o, err=%v, want 0600", info.Mode().Perm(), err)
	}

	if err := os.Chmod(statePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := queuestate.Save("mid-2", "q2", "s2"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(statePath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("queue_state.json mode after self-heal = %o, err=%v, want 0600", info.Mode().Perm(), err)
	}
}
