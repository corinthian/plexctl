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
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/playback"
	"github.com/corinthian/plexctl/internal/queuestate"
)

// v2 (docs/error_model_v2.md §2/§3/§5): every failure path below returns
// *output.CLIError instead of an ad hoc {"ok":false,...} map; the command
// layer forwards it to output.FailErr. Success maps are unchanged in shape.
//
// Queue code precedence for a bind failure (§5): PLEX_QUEUE_CONFLICT >
// PLEX_QUEUE_STAGED > PLEX_PLAYBACK_NOT_STARTED > PLEX_CLIENT_UNREACHABLE.
// Transport detail always rides data.clientUnreachable, never as the code,
// once a queue-scoped code applies — see stageBindFailure and Start.

// errNoServerID is the sentinel Add()/Create() return when
// playback.GetServerMachineID() fails. Its text is enumerated verbatim in
// the v2 code table under INTERNAL ("could not retrieve server
// machineIdentifier"), so every caller maps it to CodeInternal regardless of
// which higher-level queue operation it happened inside — mirrors
// collections.Create/addItemsInternal's identical treatment of the same
// failure.
var errNoServerID = errors.New("could not retrieve server machineIdentifier")

func noServerIDErr() *output.CLIError {
	return output.Err(output.CodeInternal, errNoServerID.Error()).WithHint("report this — plexctl bug")
}

// internalStateErr wraps a local queue-state read/write failure (disk I/O,
// permissions) that is neither STAGED (the write didn't persist) nor
// CONFLICT (no prior active record blocked it) — a plexctl-side fault, not a
// Plex domain failure. Not in the closed queue-code mapping given for this
// migration; flagged in the P2-E report rather than silently invented.
func internalStateErr(context string, err error) *output.CLIError {
	return output.Err(output.CodeInternal, context+": "+err.Error()).WithHint("report this — plexctl bug")
}

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

// noActiveQueue is the shared PLEX_NO_QUEUE error for a client with no
// resolvable saved queue. Built here exactly once (resolveQueueID, Start,
// and AddToClient all route through it).
func noActiveQueue(client jsonx.J) *output.CLIError {
	label := clientLabel(client)
	return output.Err(output.CodeNoQueue, fmt.Sprintf("no active queue on %s", label)).
		WithHint("queue something first").
		WithData("client", label)
}

// clearClientState drops the persisted (mid -> queueID) entry for the client,
// used when PMS reports the queue is gone (404).
func clearClientState(client jsonx.J) error {
	if mid := client["machineIdentifier"]; jsonx.Truthy(mid) {
		return queuestate.Clear(jsonx.AsStr(mid))
	}
	return nil
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
// rollback on mid-loop failure). Failures map to CodeQueueCreateFailed per
// docs/error_model_v2.md §2, except the server-machineIdentifier lookup,
// which is CodeInternal (enumerated verbatim in the code table) regardless
// of whether it fails before the POST or mid-loop inside Add.
func Create(ratingKeys []string, shuffle, repeat bool) (jsonx.J, *output.CLIError) {
	serverID := playback.GetServerMachineID()
	if serverID == "" {
		return nil, noServerIDErr()
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
		return nil, output.Err(output.CodeQueueCreateFailed, "playQueue creation returned no playQueueID").
			WithHint("retry the queue command")
	}
	queueIDStr := jsonx.AsStr(queueID)

	for _, rk := range ratingKeys[1:] {
		_, addErr := Add(queueIDStr, rk)
		if addErr != nil {
			rollbackQueue(queueIDStr)
			if errors.Is(addErr, errNoServerID) {
				return nil, noServerIDErr().
					WithData("partialQueueID", queueIDStr).WithData("rollbackAttempted", true)
			}
			return nil, output.Err(output.CodeQueueCreateFailed, addErr.Error()).
				WithHint("retry the queue command").
				WithData("partialQueueID", queueIDStr).WithData("rollbackAttempted", true)
		}
	}

	if selectedID == nil {
		return nil, output.Err(output.CodeQueueCreateFailed, "playQueue created but PMS returned no playQueueSelectedItemID").
			WithHint("retry the queue command")
	}
	return jsonx.J{"ok": true, "playQueueID": queueIDStr, "selectedItemID": jsonx.AsStr(selectedID)}, nil
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
func resolveQueueID(client jsonx.J) (string, *output.CLIError) {
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
// empty/missing queue is a valid state, not an error. A non-404 fetch
// failure is passed through from api.Classify unchanged, per
// docs/error_model_v2.md §3 ("don't re-wrap").
func Show(client jsonx.J) (jsonx.J, *output.CLIError) {
	qid, err := resolveQueueID(client)
	if err != nil {
		return emptyState(client), nil
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
			return emptyState(client), nil
		}
		return nil, api.Classify(api.AsError(gerr), api.TargetPMS)
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
		return jsonx.J{"ok": true, "state": "empty", "client": clientLabel(client), "items": []jsonx.J{}}, nil
	}
	return jsonx.J{"ok": true, "playQueueID": qid, "selectedItemID": selectedID, "items": items}, nil
}

// Shuffle and Unshuffle are refused outright and make NO HTTP call at all —
// not even client resolution. PMS 1.43 404s the underlying
// /playQueues/{id}/shuffle|unshuffle endpoints (verified live), and the
// skill has banned both client-side; v2 moves that refusal into the binary
// itself instead of forwarding a misleading 404 (docs/error_model_v2.md §3,
// skill_compensations.md §B).
func Shuffle() *output.CLIError {
	return output.Err(output.CodeUnsupported, "queue shuffle is not supported by the server")
}

func Unshuffle() *output.CLIError {
	return output.Err(output.CodeUnsupported, "queue shuffle is not supported by the server")
}

// Clear mirrors queue.clear. A non-404 delete failure passes through
// api.Classify unchanged (matches Show's "don't re-wrap" treatment); a local
// state-clear failure (disk I/O) is CodeInternal — see internalStateErr.
func Clear(client jsonx.J) (jsonx.J, *output.CLIError) {
	qid, err := resolveQueueID(client)
	if err != nil {
		return nil, err
	}
	if _, derr := api.TryDelete("/playQueues/"+qid+"/items", nil); derr != nil {
		// Clearing an already-pruned queue is idempotent success — still drop
		// the stale state entry.
		if apiStatus(derr) == 404 {
			if cerr := clearClientState(client); cerr != nil {
				return nil, internalStateErr("failed to clear queue state", cerr)
			}
			return jsonx.J{"ok": true}, nil
		}
		return nil, api.Classify(api.AsError(derr), api.TargetPMS)
	}
	if cerr := clearClientState(client); cerr != nil {
		return nil, internalStateErr("failed to clear queue state", cerr)
	}
	return jsonx.J{"ok": true}, nil
}

// StageOutcome is the result of attempting to persist a NEWLY CREATED queue
// via SaveIfAbsent — the shared decision behind both a bind failure and a
// bind-success-but-engagement-failure (queue never actually got claimed as
// ok:true, so it is staged rather than unconditionally saved). Exactly one
// of Staged, ActiveQueueID (non-empty), or WriteErr applies.
type StageOutcome struct {
	Staged        bool   // SaveIfAbsent wrote fresh — recoverable via queue-start
	ActiveQueueID string // set only when an existing entry was preserved (conflict shape)
	WriteErr      error  // set only on a local disk failure
}

// Stage attempts to persist queueID/selectedID for client via
// queuestate.SaveIfAbsent, never clobbering an existing (bound/possibly
// playing) entry. Shared by StageBindFailure and the command layer's own
// bind-succeeded-but-engagement-failed path — both cases mean the `queue`
// invocation is NOT claiming ok:true, so neither may use the unconditional
// Save reserved for a fully verified success.
func Stage(client jsonx.J, queueID, selectedID string) StageOutcome {
	var mid string
	if v := client["machineIdentifier"]; jsonx.Truthy(v) {
		mid = jsonx.AsStr(v)
	}
	staged, serr := queuestate.SaveIfAbsent(mid, queueID, selectedID)
	if serr != nil {
		return StageOutcome{WriteErr: serr}
	}
	if staged {
		return StageOutcome{Staged: true}
	}
	// SaveIfAbsent found an existing entry and preserved it — the v1 "dead
	// zone": a prior queue is still the active record, so the new one could
	// not be staged.
	activeQueueID := ""
	if entry := queuestate.Load(mid); entry != nil {
		activeQueueID = jsonx.AsStr(entry["playQueueID"])
	}
	return StageOutcome{ActiveQueueID: activeQueueID}
}

// StageBindFailure builds the v2 CLIError for a failed Companion bind
// against a NEWLY CREATED queue (the `queue` command only — Start's own bind
// failure is handled separately since no staging decision is needed there).
// Per docs/error_model_v2.md §5 precedence, this is always CONFLICT or
// STAGED — never a bare CLIENT_UNREACHABLE, which instead rides in
// data.clientUnreachable.
func StageBindFailure(client jsonx.J, bindErr, queueID, selectedID string) *output.CLIError {
	unreachable := playback.IsTransportError(bindErr)
	outcome := Stage(client, queueID, selectedID)
	switch {
	case outcome.WriteErr != nil:
		// Bind already failed; staging also failing is a second, distinct
		// plexctl-side problem. Not STAGED (nothing persisted) and not
		// CONFLICT (SaveIfAbsent found no prior entry to preserve) — see
		// internalStateErr's doc comment.
		return internalStateErr("bind failed ("+bindErr+") and additionally failed to stage queue state", outcome.WriteErr).
			WithData("playQueueID", queueID).WithData("selectedItemID", selectedID).
			WithData("clientUnreachable", unreachable)
	case outcome.Staged:
		return output.Err(output.CodeQueueStaged, "queue created but the client did not confirm playback ("+bindErr+")").
			WithHint("run: plexctl queue-start once the client is awake — do NOT re-run queue").
			WithData("playQueueID", queueID).WithData("selectedItemID", selectedID).
			WithData("staged", true).WithData("clientUnreachable", unreachable)
	default:
		return output.Err(output.CodeQueueConflict, "queue created but could not be staged — a previous queue is still the active record ("+bindErr+")").
			WithHint("re-run the queue command once the client is back — queue-start would start the OLD queue").
			WithData("activeQueueID", outcome.ActiveQueueID).WithData("orphanedQueueID", queueID).
			WithData("clientUnreachable", unreachable)
	}
}

// Start binds the saved/staged play queue for the client and starts it — the
// recovery path after a bind failure ("run it again when the device is
// back" becomes queue-start, not a queue-recreating retry). No saved entry
// → CodeNoQueue. A bind failure here always maps to CodeQueueStaged (never
// CONFLICT: there is only ever one persisted entry per client, and Start
// leaves it untouched either way, so it stays the recoverable record).
func Start(client jsonx.J) (jsonx.J, *output.CLIError) {
	var entry jsonx.J
	if mid := client["machineIdentifier"]; jsonx.Truthy(mid) {
		entry = queuestate.Load(jsonx.AsStr(mid))
	}
	if entry == nil || !jsonx.Truthy(entry["playQueueID"]) {
		return nil, noActiveQueue(client)
	}
	queueID := jsonx.AsStr(entry["playQueueID"])
	var selectedItemID string
	if jsonx.Truthy(entry["selectedItemID"]) {
		selectedItemID = jsonx.AsStr(entry["selectedItemID"])
	}
	bind := playback.PlayQueue(client, queueID, selectedItemID)
	if !jsonx.Truthy(bind["ok"]) {
		errStr, _ := bind["error"].(string)
		unreachable := playback.IsTransportError(errStr)
		return nil, output.Err(output.CodeQueueStaged, "queue-start bind failed ("+errStr+")").
			WithHint("run: plexctl queue-start once the client is awake — do NOT re-run queue").
			WithData("playQueueID", queueID).WithData("selectedItemID", selectedItemID).
			WithData("staged", true).WithData("clientUnreachable", unreachable)
	}
	return jsonx.J{"ok": true, "playQueueID": queueID, "selectedItemID": selectedItemID}, nil
}

// RemoveItem mirrors queue.remove_item. The DELETE itself is print-and-exit
// api.Delete, already classified through the ExitOnError chokepoint on
// failure.
func RemoveItem(client jsonx.J, itemID string) (jsonx.J, *output.CLIError) {
	qid, err := resolveQueueID(client)
	if err != nil {
		return nil, err
	}
	api.Delete("/playQueues/"+qid+"/items/"+itemID, nil)
	return jsonx.J{"ok": true}, nil
}

// Add mirrors queue.add (single PUT append to a known queue id). Uses
// api.TryPut, not print-and-exit api.Put: a mid-loop transport or HTTP
// failure must return an error so Create's rollback and AddToClient's
// per-key error reporting are both reachable on a real failure, not just the
// "HTTP 200 but no playQueueID" shape. Stays a low-level helper returning a
// plain error — Create and AddToClient each recode a failure into their own
// queue-scoped CLIError (CodeQueueCreateFailed vs CodeQueuePartial), except
// errNoServerID, which both map to CodeInternal identically.
func Add(queueID, ratingKey string) (jsonx.J, error) {
	serverID := playback.GetServerMachineID()
	if serverID == "" {
		return nil, errNoServerID
	}

	uri := fmt.Sprintf("server://%s/com.plexapp.plugins.library/library/metadata/%s", serverID, ratingKey)
	params := url.Values{}
	params.Set("uri", uri)
	data, err := api.TryPut("/playQueues/"+queueID, params)
	if err != nil {
		return nil, err
	}
	mc := jsonx.GetMap(data, "MediaContainer")
	if jsonx.Truthy(mc["playQueueID"]) {
		return jsonx.J{"ok": true}, nil
	}
	return nil, errors.New("add to queue returned unexpected response")
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
// size growth, so we re-read the queue after each add and bail with
// CodeNotApplied (exit 6) when the size didn't move. Cost: one extra GET per
// add — acceptable for the typical 1-3 keys / voice request.
func AddToClient(client jsonx.J, ratingKeys []string) (jsonx.J, *output.CLIError) {
	if len(ratingKeys) == 0 {
		return nil, output.Err(output.CodeBadRequest, "add requires at least one ratingKey")
	}
	qid, err := resolveQueueID(client)
	if err != nil {
		return nil, err
	}
	sizeData, serr := api.TryGet("/playQueues/"+qid, nil)
	if serr != nil {
		// A 404 means the saved id no longer resolves. Report no-active-queue,
		// but do NOT delete the saved entry: a transient 404 must not destroy
		// an addressable queue (finding 7); the next successful queue Save
		// self-heals a genuinely stale entry.
		if apiStatus(serr) == 404 {
			return nil, noActiveQueue(client)
		}
		return nil, api.Classify(api.AsError(serr), api.TargetPMS)
	}
	expected := int(jsonx.Num(jsonx.GetMap(sizeData, "MediaContainer")["size"]))
	added := 0
	for _, rk := range ratingKeys {
		_, addErr := Add(qid, rk)
		if addErr != nil {
			if errors.Is(addErr, errNoServerID) {
				return nil, noServerIDErr().WithData("added", added).WithData("playQueueID", qid)
			}
			return nil, output.Err(output.CodeQueuePartial, addErr.Error()).
				WithHint("retry with the remaining items").
				WithData("added", added).WithData("failedKey", rk).WithData("playQueueID", qid)
		}
		expected++
		verifyData, verr := api.TryGet("/playQueues/"+qid, nil)
		if verr != nil {
			return nil, output.Err(output.CodeQueuePartial, verr.Error()).
				WithHint("retry with the remaining items").
				WithData("added", added).WithData("failedKey", rk).WithData("playQueueID", qid)
		}
		actual := int(jsonx.Num(jsonx.GetMap(verifyData, "MediaContainer")["size"]))
		if actual != expected {
			return nil, output.Err(output.CodeNotApplied,
				"PMS accepted the add but the queue did not grow — ratingKey likely unknown or invalid").
				WithData("ratingKey", rk)
		}
		added++
	}
	return jsonx.J{"ok": true, "added": added, "playQueueID": qid}, nil
}
