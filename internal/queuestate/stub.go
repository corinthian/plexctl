// Package queuestate ports plexctl/queue_state.py: the per-client
// playQueueID persistence (queue_state.json next to config.toml, atomic
// tmp+rename writes, same schema).
package queuestate

import "github.com/corinthian/plexctl/internal/jsonx"

// Save mirrors queue_state.save (no-op on empty mid/queueID).
func Save(clientMID, queueID, selectedID string) { panic("not ported: queuestate.Save") }

// Load mirrors queue_state.load; nil when absent.
func Load(clientMID string) jsonx.J { panic("not ported: queuestate.Load") }

// Clear mirrors queue_state.clear.
func Clear(clientMID string) { panic("not ported: queuestate.Clear") }
