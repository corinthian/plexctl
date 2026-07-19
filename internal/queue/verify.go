package queue

import (
	"time"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/jsonx"
)

// A Companion playMedia 200 means the device's listener answered, not that
// the Plex app engaged playback — the listener can accept commands the app
// never acts on (incidents 2026-07-05, 2026-07-13, both with active:true and
// a reachable baseurl). ConfirmEngaged closes that gap by polling the
// server's sessions until the client itself reports an engaged player.

// Verify pacing. Vars are seams for tests (like output.Stdout/Exit);
// production never overrides them. The first probe is immediate, so the
// happy path costs one sessions GET and no sleep.
var (
	VerifyProbes = 12
	VerifySleep  = time.Sleep
)

const verifyInterval = 500 * time.Millisecond

// ConfirmEngaged reports whether the client's PMS session shows a non-idle
// player within the verify window — playing one of the allowed ratingKeys
// when allowed is non-empty (a session already running something else must
// not count as engagement). Session state and ratingKey originate from the
// client's own timeline reports to PMS, so a match cannot be produced by a
// command that was accepted but never acted on. An empty allowed list
// accepts any non-idle session (degraded mode when the queue's items are
// unknown). A client with no machineIdentifier is unverifiable: report
// engaged rather than invent a failure for playback that may be running.
func ConfirmEngaged(client jsonx.J, allowed []string) bool {
	mid := client["machineIdentifier"]
	if !jsonx.Truthy(mid) {
		return true
	}
	for i := 0; i < VerifyProbes; i++ {
		if i > 0 {
			VerifySleep(verifyInterval)
		}
		data, err := api.TryGet("/status/sessions", nil)
		if err != nil {
			// Transient sessions failure must not abort a bind that already
			// succeeded; keep polling for the rest of the window.
			continue
		}
		for _, s := range jsonx.MapList(jsonx.GetMap(data, "MediaContainer"), "Metadata") {
			player := jsonx.GetMap(s, "Player")
			if player["machineIdentifier"] != mid {
				continue
			}
			state, _ := player["state"].(string)
			if state == "" || state == "idle" {
				continue
			}
			if len(allowed) == 0 {
				return true
			}
			rk := jsonx.AsStr(s["ratingKey"])
			for _, k := range allowed {
				if k == rk {
					return true
				}
			}
		}
	}
	return false
}

// ItemRatingKeys returns the ratingKeys of the queue's current items, nil on
// any fetch failure (callers degrade to unscoped verification rather than
// fail a bind that already succeeded).
func ItemRatingKeys(queueID string) []string {
	data, err := api.TryGet("/playQueues/"+queueID, nil)
	if err != nil {
		return nil
	}
	items := jsonx.MapList(jsonx.GetMap(data, "MediaContainer"), "Metadata")
	keys := make([]string, 0, len(items))
	for _, item := range items {
		if jsonx.Truthy(item["ratingKey"]) {
			keys = append(keys, jsonx.AsStr(item["ratingKey"]))
		}
	}
	return keys
}
