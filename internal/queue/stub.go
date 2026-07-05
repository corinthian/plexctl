// Package queue ports plexctl/queue.py: /playQueues create with server://
// URIs and rollback, state-file queue resolution, and the size-delta
// validated add path.
package queue

import "github.com/corinthian/plexctl/internal/jsonx"

// Create mirrors queue.create (continuous=1 only for single-key seeds;
// rollback + partialQueueID on mid-loop failure).
func Create(ratingKeys []string, shuffle, repeat bool) jsonx.J {
	panic("not ported: queue.Create")
}

// Show mirrors queue.show (empty queue is ok:true, state empty).
func Show(client jsonx.J) jsonx.J { panic("not ported: queue.Show") }

// Shuffle / Unshuffle / Clear / RemoveItem mirror their Python namesakes.
func Shuffle(client jsonx.J) jsonx.J   { panic("not ported: queue.Shuffle") }
func Unshuffle(client jsonx.J) jsonx.J { panic("not ported: queue.Unshuffle") }
func Clear(client jsonx.J) jsonx.J     { panic("not ported: queue.Clear") }
func RemoveItem(client jsonx.J, itemID string) jsonx.J {
	panic("not ported: queue.RemoveItem")
}

// Add mirrors queue.add (single PUT append to a known queue id).
func Add(queueID, ratingKey string) jsonx.J { panic("not ported: queue.Add") }

// AddToClient mirrors queue.add_to_client (size-delta validation per key).
func AddToClient(client jsonx.J, ratingKeys []string) jsonx.J {
	panic("not ported: queue.AddToClient")
}
