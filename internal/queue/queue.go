// Package queue ports plexctl/queue.py: /playQueues create with server://
// URIs and rollback, state-file queue resolution, and the size-delta
// validated add path.
package queue

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/playback"
	"github.com/corinthian/plexctl/internal/queuestate"
)

// apiStatus returns the HTTP status carried by an *api.Error (0 for
// transport/parse errors or any non-*api.Error). A 404 on a saved-queue
// dereference means PMS pruned the queue out from under us.
func apiStatus(err error) int {
	var e *api.Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// noActiveQueue is the shared ok:false error for a client with no resolvable
// saved queue. The literal lives here exactly once (resolveQueueID, Start, and
// AddToClient all route through it).
func noActiveQueue(client jsonx.J) jsonx.J {
	return jsonx.J{"ok": false, "error": fmt.Sprintf("no active queue on %s", clientLabel(client))}
}

// AnnotateBind attaches the queue's IDs to a bind result and, on a transport-
// shaped failure (the device didn't answer), flags clientUnreachable. Shared by
// the queue command's RunE and queue.Start so both surface a bind outcome
// identically. It never sets clientUnreachable for an HTTP-status bind error.
func AnnotateBind(result jsonx.J, queueID, selectedID string) {
	result["playQueueID"] = queueID
	result["selectedItemID"] = selectedID
	if !jsonx.Truthy(result["ok"]) {
		if errStr, _ := result["error"].(string); playback.IsTransportError(errStr) {
			result["clientUnreachable"] = true
		}
	}
}

// clearClientState drops the persisted (mid -> queueID) entry for the client,
// used when PMS reports the queue is gone (404).
func clearClientState(client jsonx.J) {
	if mid := client["machineIdentifier"]; jsonx.Truthy(mid) {
		queuestate.Clear(jsonx.AsStr(mid))
	}
}

// emptyState is the ok:true "no queue here" shape shared by the resolver-miss
// and pruned-queue (404) paths.
func emptyState(client jsonx.J) jsonx.J {
	return jsonx.J{"ok": true, "state": "empty", "client": clientLabel(client), "items": []jsonx.J{}}
}

// flag01 renders a bool as the "1"/"0" query param PMS expects.
func flag01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// rollbackQueue is a best-effort deletion of a partially-built play queue.
// Never raises.
func rollbackQueue(queueID string) {
	_, _ = api.TryDelete("/playQueues/"+queueID, nil)
}

// Create mirrors queue.create (continuous=1 only for single-key seeds;
// rollback + partialQueueID on mid-loop failure).
func Create(ratingKeys []string, shuffle, repeat bool) jsonx.J {
	serverID := playback.GetServerMachineID()
	if serverID == "" {
		return jsonx.J{"ok": false, "error": "could not retrieve server machineIdentifier"}
	}

	firstKey := ratingKeys[0]
	uri := fmt.Sprintf("server://%s/com.plexapp.plugins.library/library/metadata/%s", serverID, firstKey)

	// `continuous: 1` tells PMS to auto-fill the queue with the seed key's
	// show siblings. That's the right UX for single-key "play this and keep
	// going" intents, but for multi-key requests the user has explicitly
	// picked the contents — PMS would auto-fill from the FIRST key and then
	// the subsequent PUT-adds layer duplicates on top of the auto-fill.
	// Verified 2026-05-13 against PMS 1.43.0.
	continuous := "0"
	if len(ratingKeys) == 1 {
		continuous = "1"
	}
	params := url.Values{}
	params.Set("type", "video")
	params.Set("uri", uri)
	params.Set("shuffle", flag01(shuffle))
	params.Set("repeat", flag01(repeat))
	params.Set("continuous", continuous)
	data := api.Post("/playQueues", params)

	mc := jsonx.GetMap(data, "MediaContainer")
	queueID := mc["playQueueID"]
	selectedID := mc["playQueueSelectedItemID"]

	if !jsonx.Truthy(queueID) {
		return jsonx.J{"ok": false, "error": "playQueue creation returned no playQueueID"}
	}

	for _, rk := range ratingKeys[1:] {
		result := Add(jsonx.AsStr(queueID), rk)
		if !jsonx.Truthy(result["ok"]) {
			rollbackQueue(jsonx.AsStr(queueID))
			result["partialQueueID"] = jsonx.AsStr(queueID)
			result["rollbackAttempted"] = true
			return result
		}
	}

	if selectedID == nil {
		return jsonx.J{"ok": false, "error": "playQueue created but PMS returned no playQueueSelectedItemID"}
	}
	return jsonx.J{"ok": true, "playQueueID": jsonx.AsStr(queueID), "selectedItemID": jsonx.AsStr(selectedID)}
}

// clientLabel mirrors queue._client_label.
func clientLabel(client jsonx.J) string {
	if v := client["name"]; jsonx.Truthy(v) {
		return jsonx.AsStr(v)
	}
	if v := client["machineIdentifier"]; jsonx.Truthy(v) {
		return jsonx.AsStr(v)
	}
	return "client"
}

// PMS exposes no server-side endpoint for discovering an active playQueueID
// (verified against the OpenAPI 3.1.0 spec for PMS 1.2.0). The Companion
// `/timeline/poll` endpoint that would return it rejects with HTTP 400 on
// Apple TV 8.45. Instead, plexctl persists `(client_mid -> playQueueID)` to
// disk on queue creation and reads it back here.
func resolveQueueID(client jsonx.J) (string, jsonx.J) {
	var entry jsonx.J
	if mid := client["machineIdentifier"]; jsonx.Truthy(mid) {
		entry = queuestate.Load(jsonx.AsStr(mid))
	}
	if entry != nil && jsonx.Truthy(entry["playQueueID"]) {
		return jsonx.AsStr(entry["playQueueID"]), nil
	}
	return "", noActiveQueue(client)
}

// Show mirrors queue.show (empty queue is ok:true, state empty). An
// empty/missing queue is a valid state, not an error.
func Show(client jsonx.J) jsonx.J {
	qid, err := resolveQueueID(client)
	if err != nil {
		return emptyState(client)
	}
	data, gerr := api.TryGet("/playQueues/"+qid, nil)
	if gerr != nil {
		// A 404 means our saved id no longer resolves — a genuine prune, or a
		// transient/proxy/restart 404. Degrade to the same empty state as a
		// never-created queue, but do NOT delete the saved entry: a transient
		// 404 must not destroy the only record of an addressable queue
		// (finding 7). The next successful `queue` Save self-heals a genuinely
		// stale entry.
		if apiStatus(gerr) == 404 {
			return emptyState(client)
		}
		return jsonx.J{"ok": false, "error": gerr.Error()}
	}
	mc := jsonx.GetMap(data, "MediaContainer")
	selectedID := mc["playQueueSelectedItemID"]
	rawItems := jsonx.MapList(mc, "Metadata")
	items := make([]jsonx.J, 0, len(rawItems))
	for _, item := range rawItems {
		items = append(items, jsonx.J{
			"playQueueItemID": item["playQueueItemID"],
			"title":           item["title"],
			"type":            item["type"],
			"year":            item["year"],
			"duration":        item["duration"],
			"selected":        item["playQueueItemID"] == selectedID,
		})
	}
	if len(items) == 0 {
		return jsonx.J{"ok": true, "state": "empty", "client": clientLabel(client), "items": []jsonx.J{}}
	}
	return jsonx.J{"ok": true, "playQueueID": qid, "selectedItemID": selectedID, "items": items}
}

// Shuffle mirrors queue.shuffle.
func Shuffle(client jsonx.J) jsonx.J {
	qid, err := resolveQueueID(client)
	if err != nil {
		return err
	}
	api.Put("/playQueues/"+qid+"/shuffle", nil)
	return jsonx.J{"ok": true}
}

// Unshuffle mirrors queue.unshuffle.
func Unshuffle(client jsonx.J) jsonx.J {
	qid, err := resolveQueueID(client)
	if err != nil {
		return err
	}
	api.Put("/playQueues/"+qid+"/unshuffle", nil)
	return jsonx.J{"ok": true}
}

// Clear mirrors queue.clear.
func Clear(client jsonx.J) jsonx.J {
	qid, err := resolveQueueID(client)
	if err != nil {
		return err
	}
	if _, derr := api.TryDelete("/playQueues/"+qid+"/items", nil); derr != nil {
		// Clearing an already-pruned queue is idempotent success — still drop
		// the stale state entry.
		if apiStatus(derr) == 404 {
			clearClientState(client)
			return jsonx.J{"ok": true}
		}
		return jsonx.J{"ok": false, "error": derr.Error()}
	}
	clearClientState(client)
	return jsonx.J{"ok": true}
}

// Start binds the saved/staged play queue for the client and starts it — the
// recovery path after a bind failure ("run it again when the device is back"
// becomes queue-start, not a queue-recreating retry). No saved entry → the
// same no-active-queue error the skill already translates. On success the
// state is kept; on bind failure it returns the same staged result shape as
// queue (IDs + clientUnreachable for transport errors) and keeps the state so
// it can be retried.
func Start(client jsonx.J) jsonx.J {
	var entry jsonx.J
	if mid := client["machineIdentifier"]; jsonx.Truthy(mid) {
		entry = queuestate.Load(jsonx.AsStr(mid))
	}
	if entry == nil || !jsonx.Truthy(entry["playQueueID"]) {
		return noActiveQueue(client)
	}
	queueID := jsonx.AsStr(entry["playQueueID"])
	selectedItemID := jsonx.AsStr(entry["selectedItemID"])
	result := playback.PlayQueue(client, queueID, selectedItemID)
	AnnotateBind(result, queueID, selectedItemID)
	return result
}

// RemoveItem mirrors queue.remove_item.
func RemoveItem(client jsonx.J, itemID string) jsonx.J {
	qid, err := resolveQueueID(client)
	if err != nil {
		return err
	}
	api.Delete("/playQueues/"+qid+"/items/"+itemID, nil)
	return jsonx.J{"ok": true}
}

// Add mirrors queue.add (single PUT append to a known queue id).
func Add(queueID, ratingKey string) jsonx.J {
	serverID := playback.GetServerMachineID()
	if serverID == "" {
		return jsonx.J{"ok": false, "error": "could not retrieve server machineIdentifier"}
	}

	uri := fmt.Sprintf("server://%s/com.plexapp.plugins.library/library/metadata/%s", serverID, ratingKey)
	params := url.Values{}
	params.Set("uri", uri)
	data := api.Put("/playQueues/"+queueID, params)
	mc := jsonx.GetMap(data, "MediaContainer")
	if jsonx.Truthy(mc["playQueueID"]) {
		return jsonx.J{"ok": true}
	}
	return jsonx.J{"ok": false, "error": "add to queue returned unexpected response"}
}

// AddToClient mirrors queue.add_to_client (size-delta validation per key).
//
// The queue is resolved from the persisted (client_mid -> playQueueID)
// state, same as Show/Clear/RemoveItem. No rollback on mid-loop failure —
// the queue is user-managed state, not freshly created by this call, so
// rolling back would clobber pre-existing items.
//
// PMS quirk: PUT /playQueues/{id}?uri=... returns 200 + a valid
// MediaContainer even when the ratingKey doesn't exist (verified live
// 2026-05-13 against PMS 1.43.0). The only reliable success signal is queue
// size growth, so we re-read the queue after each add and bail with a clear
// error when the size didn't move. Cost: one extra GET per add — acceptable
// for the typical 1-3 keys / voice request.
func AddToClient(client jsonx.J, ratingKeys []string) jsonx.J {
	if len(ratingKeys) == 0 {
		return jsonx.J{"ok": false, "error": "add requires at least one ratingKey"}
	}
	qid, err := resolveQueueID(client)
	if err != nil {
		return err
	}
	sizeData, serr := api.TryGet("/playQueues/"+qid, nil)
	if serr != nil {
		// A 404 means the saved id no longer resolves. Report no-active-queue,
		// but do NOT delete the saved entry: a transient 404 must not destroy an
		// addressable queue (finding 7); the next successful queue Save
		// self-heals a genuinely stale entry.
		if apiStatus(serr) == 404 {
			return noActiveQueue(client)
		}
		return jsonx.J{"ok": false, "error": serr.Error()}
	}
	expected := int(jsonx.Num(jsonx.GetMap(sizeData, "MediaContainer")["size"]))
	added := 0
	for _, rk := range ratingKeys {
		result := Add(qid, rk)
		if !jsonx.Truthy(result["ok"]) {
			result["added"] = added
			result["playQueueID"] = qid
			return result
		}
		expected++
		verifyData, verr := api.TryGet("/playQueues/"+qid, nil)
		if verr != nil {
			return jsonx.J{"ok": false, "error": verr.Error(), "added": added, "playQueueID": qid}
		}
		actual := int(jsonx.Num(jsonx.GetMap(verifyData, "MediaContainer")["size"]))
		if actual != expected {
			return jsonx.J{
				"ok":          false,
				"error":       fmt.Sprintf("PMS accepted PUT but did not add ratingKey %s — likely unknown or invalid", rk),
				"added":       added,
				"playQueueID": qid,
			}
		}
		added++
	}
	return jsonx.J{"ok": true, "added": added, "playQueueID": qid}
}
