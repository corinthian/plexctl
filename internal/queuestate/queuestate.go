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
	"time"

	"github.com/corinthian/plexctl/internal/config"
	"github.com/corinthian/plexctl/internal/jsonx"
)

func path() string {
	return filepath.Join(config.Dir(), "queue_state.json")
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

func writeAll(state jsonx.J) {
	p := path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// Save mirrors queue_state.save: no-op on empty mid/queueID; selectedID ""
// writes JSON null (Python None).
func Save(clientMID, queueID, selectedID string) {
	if clientMID == "" || queueID == "" {
		return
	}
	var selected any
	if selectedID != "" {
		selected = selectedID
	}
	state := readAll()
	state[clientMID] = jsonx.J{
		"playQueueID":    queueID,
		"selectedItemID": selected,
		"savedAt":        time.Now().Unix(),
	}
	writeAll(state)
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
func Clear(clientMID string) {
	if clientMID == "" {
		return
	}
	state := readAll()
	if _, ok := state[clientMID]; ok {
		delete(state, clientMID)
		writeAll(state)
	}
}
