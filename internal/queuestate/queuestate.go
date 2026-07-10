// Package queuestate ports plexctl/queue_state.py: persist playQueueID per
// client so plexctl can resolve queues without the Companion /timeline/poll
// endpoint (HTTP 400 on Apple TV 8.45). PMS does not expose an active
// queue-id via any GET endpoint, so plexctl remembers what it created.
//
// State file lives next to config.toml. Schema (unchanged from Python; the
// cutover keeps the live file):
//
//	{
//	  "<client_machineIdentifier>": {
//	    "playQueueID": "5535",
//	    "selectedItemID": "42687",
//	    "savedAt": 1762290000
//	  }
//	}
package queuestate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/corinthian/plexctl/internal/config"
	"github.com/corinthian/plexctl/internal/jsonx"
)

func path() string {
	return filepath.Join(config.Dir(), "queue_state.json")
}

func lockPath() string {
	return filepath.Join(config.Dir(), "queue_state.lock")
}

// withLock runs fn while holding an exclusive flock on queue_state.lock — a
// stable inode kept separate from queue_state.json, which is rewritten via
// temp+rename (its inode changes every write), mirroring the commandid lock
// pattern. It serializes the read-modify-write in Save/SaveIfAbsent/Clear so
// two concurrent commands (e.g. iPad /remote-control plus macOS) cannot lose an
// update. Lock-acquisition FAILURE (mkdir/open/flock error) degrades to running
// fn unlocked: a command must never fail because a lock couldn't be taken. Each
// mutator calls withLock exactly once and never nests, so the same-process
// two-fd flock self-deadlock can't arise.
func withLock(fn func() error) error {
	if err := os.MkdirAll(config.Dir(), 0o755); err != nil {
		return fn()
	}
	lockFile, err := os.OpenFile(lockPath(), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fn()
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fn()
	}
	defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) }()
	return fn()
}

func readAll() jsonx.J {
	b, err := os.ReadFile(path())
	if err != nil {
		return jsonx.J{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return jsonx.J{}
	}
	return m
}

func writeAll(state jsonx.J) error {
	p := path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp) // best-effort: don't leave a stale .tmp behind on a failed rename
		return err
	}
	return nil
}

// Save mirrors queue_state.save: no-op on empty mid/queueID; selectedID ""
// writes JSON null (Python None). Returns the write error, if any — the
// success path of every `queue` depends on this: a silent failure here
// used to mean the bound queue was never recorded and every later
// queue-show/queue-add/shuffle would answer "no active queue" with no
// explanation (see the D2 ruling on how the command layer reports this).
func Save(clientMID, queueID, selectedID string) error {
	if clientMID == "" || queueID == "" {
		return nil
	}
	var selected any
	if selectedID != "" {
		selected = selectedID
	}
	return withLock(func() error {
		state := readAll()
		state[clientMID] = jsonx.J{
			"playQueueID":    queueID,
			"selectedItemID": selected,
			"savedAt":        time.Now().Unix(),
		}
		return writeAll(state)
	})
}

// SaveIfAbsent writes the entry only when the client has no entry yet, and
// returns true exactly when it wrote. The bind-failure path uses it to stage a
// newly created queue without clobbering an existing (bound/possibly playing)
// one — the returned bool is the single source of truth for the caller's
// `staged` flag, so the flag and the persisted state can never disagree: it
// is set only after writeAll itself succeeds, never unconditionally after
// attempting the write. Empty mid/queueID is a no-op → (false, nil). It is
// its own read-modify-write (never composed from Save) so C5's per-op lock
// can wrap it without self-deadlock.
func SaveIfAbsent(clientMID, queueID, selectedID string) (bool, error) {
	if clientMID == "" || queueID == "" {
		return false, nil
	}
	var selected any
	if selectedID != "" {
		selected = selectedID
	}
	wrote := false
	err := withLock(func() error {
		state := readAll()
		if _, ok := state[clientMID]; ok {
			return nil
		}
		state[clientMID] = jsonx.J{
			"playQueueID":    queueID,
			"selectedItemID": selected,
			"savedAt":        time.Now().Unix(),
		}
		if err := writeAll(state); err != nil {
			return err
		}
		wrote = true
		return nil
	})
	return wrote, err
}

// Load mirrors queue_state.load; nil when absent.
func Load(clientMID string) jsonx.J {
	if clientMID == "" {
		return nil
	}
	if entry, ok := readAll()[clientMID].(map[string]any); ok {
		return entry
	}
	return nil
}

// Clear mirrors queue_state.clear.
func Clear(clientMID string) error {
	if clientMID == "" {
		return nil
	}
	return withLock(func() error {
		state := readAll()
		if _, ok := state[clientMID]; ok {
			delete(state, clientMID)
			return writeAll(state)
		}
		return nil
	})
}
