// Package playlists ports plexctl/playlists.py: account-level playlists with
// playlistItemID mutation handles and the smart guard.
//
// PMS playlists are account-level (not bound to a single section). Smart
// playlists are query-driven and reject mutation; callers are expected to
// check the `smart` flag from ListAll before issuing edits.
package playlists

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/playback"
)

var validTypes = map[string]bool{"audio": true, "video": true, "photo": true}

// invalidPlaylistTypeMsg mirrors Python's f"playlist_type must be one of
// {sorted(_VALID_TYPES)}" — sorted({"audio","video","photo"}) is
// alphabetical.
const invalidPlaylistTypeMsg = "playlist_type must be one of ['audio', 'photo', 'video']"

const smartRefusal = "smart playlist: contents are query-driven and cannot be edited via " +
	"the API — edit the smart filter in the Plex app instead"

const smartContainerHint = "edit the smart rule in the Plex app"

// smartContainerErr builds the v2 PLEX_SMART_CONTAINER refusal for
// playlists (docs/error_model_v2.md §2/§3).
func smartContainerErr() *output.CLIError {
	return output.Err(output.CodeSmartContainer, smartRefusal).
		WithData("kind", "playlist").
		WithHint(smartContainerHint)
}

func uriFor(serverID, ratingKey string) string {
	return fmt.Sprintf("server://%s/com.plexapp.plugins.library/library/metadata/%s", serverID, ratingKey)
}

// isSmart is a best-effort smart-playlist check. Returns false on lookup
// failure so the real mutation surfaces the canonical error rather than this
// probe.
//
// PMS 1.43 silently 2xx-no-ops mutations against smart playlists in at least
// one verified path (smart collection add — same module pattern). Guard is
// defense-in-depth against the JSON `ok: true` contract lying.
func isSmart(playlistKey string) bool {
	r, err := api.TryGet(fmt.Sprintf("/playlists/%s", playlistKey), nil)
	if err != nil {
		return false
	}
	items := jsonx.MapList(jsonx.GetMap(r, "MediaContainer"), "Metadata")
	if len(items) == 0 {
		return false
	}
	return jsonx.Truthy(items[0]["smart"])
}

// ListAll mirrors playlists.list_all; the error mirrors its ValueError on a
// bad type. Per docs/error_model_v2.md §3, the command layer classifies
// whatever error comes back here: an *api.Error routes through
// api.Classify(…, api.TargetPMS); anything else (including this package's
// own invalid-type error) is a plexctl-internal surprise. playlistType == ""
// means unfiltered.
func ListAll(playlistType string) ([]jsonx.J, error) {
	params := url.Values{}
	if playlistType != "" {
		if !validTypes[playlistType] {
			return nil, errors.New(invalidPlaylistTypeMsg)
		}
		params.Set("playlistType", playlistType)
	}
	resp := api.Get("/playlists", params)
	items := jsonx.MapList(jsonx.GetMap(resp, "MediaContainer"), "Metadata")
	rows := make([]jsonx.J, 0, len(items))
	for _, i := range items {
		rows = append(rows, jsonx.J{
			"ratingKey":    i["ratingKey"],
			"title":        i["title"],
			"playlistType": i["playlistType"],
			"smart":        jsonx.Truthy(i["smart"]),
			"leafCount":    i["leafCount"],
			"duration":     i["duration"],
		})
	}
	return rows, nil
}

// Show mirrors playlists.show: return the items in a playlist.
func Show(ratingKey string) []jsonx.J {
	resp := api.Get(fmt.Sprintf("/playlists/%s/items", ratingKey), nil)
	items := jsonx.MapList(jsonx.GetMap(resp, "MediaContainer"), "Metadata")
	rows := make([]jsonx.J, 0, len(items))
	for _, i := range items {
		viewCount, ok := i["viewCount"]
		if !ok {
			viewCount = 0
		}
		rows = append(rows, jsonx.J{
			"playlistItemID": i["playlistItemID"],
			"ratingKey":      i["ratingKey"],
			"title":          i["title"],
			"type":           i["type"],
			"year":           i["year"],
			"duration":       i["duration"],
			"viewCount":      viewCount,
		})
	}
	return rows
}

// --- mutations ---------------------------------------------------------------

// Create mirrors playlists.create: create a manual playlist seeded with
// ratingKeys.
//
// Smart playlists are not supported — they require a search URI and are
// rarely needed from a voice interface. POSTs the first item, then PUTs each
// remaining key. On a mid-loop add failure the partial playlist is deleted
// best-effort and the CLIError's Data carries partialPlaylistID /
// rollbackAttempted: true so the caller can surface what server-side state
// existed.
func Create(title, playlistType string, ratingKeys []string) (jsonx.J, *output.CLIError) {
	if len(ratingKeys) == 0 {
		return nil, output.Err(output.CodeBadRequest, "create requires at least one ratingKey")
	}
	if !validTypes[playlistType] {
		return nil, output.Err(output.CodeBadRequest, invalidPlaylistTypeMsg)
	}
	serverID := playback.GetServerMachineID()
	if serverID == "" {
		return nil, output.Err(output.CodeInternal, "could not retrieve server machineIdentifier")
	}

	data := api.Post("/playlists", url.Values{
		"title": {title},
		"type":  {playlistType},
		"smart": {"0"},
		"uri":   {uriFor(serverID, ratingKeys[0])},
	})
	items := jsonx.MapList(jsonx.GetMap(data, "MediaContainer"), "Metadata")
	if len(items) == 0 {
		return nil, output.Err(output.CodeInternal, "playlist creation returned no metadata")
	}
	playlistKey := items[0]["ratingKey"]
	if !jsonx.Truthy(playlistKey) {
		return nil, output.Err(output.CodeInternal, "playlist creation returned no ratingKey")
	}
	key := jsonx.AsStr(playlistKey)

	for _, rk := range ratingKeys[1:] {
		_, cliErr := addItemsInternal(key, []string{rk}, serverID, true)
		if cliErr != nil {
			_, _ = api.TryDelete(fmt.Sprintf("/playlists/%s", key), nil)
			cliErr.WithData("partialPlaylistID", key).WithData("rollbackAttempted", true)
			return nil, cliErr
		}
	}

	return jsonx.J{"ok": true, "ratingKey": key, "title": title, "count": len(ratingKeys)}, nil
}

// Delete mirrors playlists.delete. Allowed on smart playlists — deletion is
// unambiguous. No local failure branch — the underlying api.Delete already
// exits through the migrated ExitOnError chokepoint on an HTTP failure.
func Delete(playlistKey string) jsonx.J {
	api.Delete(fmt.Sprintf("/playlists/%s", playlistKey), nil)
	return jsonx.J{"ok": true}
}

// Rename mirrors playlists.rename. Refuses smart playlists.
func Rename(playlistKey, newTitle string) (jsonx.J, *output.CLIError) {
	if isSmart(playlistKey) {
		return nil, smartContainerErr()
	}
	api.Put(fmt.Sprintf("/playlists/%s", playlistKey), url.Values{"title": {newTitle}})
	return jsonx.J{"ok": true}, nil
}

// addItemsInternal mirrors playlists.add_items. Per-item api.TryPut (not
// print-and-exit api.Put) so a transport or HTTP failure mid-loop returns a
// CodeQueuePartial CLIError with the count added so far, instead of killing
// the process — this is what makes Create's mid-loop rollback above
// reachable on a real failure, not just a monkeypatched one.
func addItemsInternal(playlistKey string, ratingKeys []string, serverID string, trustManual bool) (jsonx.J, *output.CLIError) {
	if len(ratingKeys) == 0 {
		return nil, output.Err(output.CodeBadRequest, "add requires at least one ratingKey")
	}
	if !trustManual && isSmart(playlistKey) {
		return nil, smartContainerErr()
	}
	sid := serverID
	if sid == "" {
		sid = playback.GetServerMachineID()
	}
	if sid == "" {
		return nil, output.Err(output.CodeInternal, "could not retrieve server machineIdentifier")
	}
	added := 0
	for _, rk := range ratingKeys {
		if _, err := api.TryPut(fmt.Sprintf("/playlists/%s/items", playlistKey), url.Values{"uri": {uriFor(sid, rk)}}); err != nil {
			return nil, output.Err(output.CodeQueuePartial, err.Error()).
				WithHint("retry with the remaining items").
				WithData("added", added).
				WithData("failedKey", rk)
		}
		added++
	}
	return jsonx.J{"ok": true, "added": added}, nil
}

// AddItems mirrors playlists.add_items (public entry point: no server-id
// override, no trust of the smart guard).
func AddItems(playlistKey string, ratingKeys []string) (jsonx.J, *output.CLIError) {
	return addItemsInternal(playlistKey, ratingKeys, "", false)
}

// RemoveItem mirrors playlists.remove_item: remove one item from a playlist
// by its playlistItemID (the per-playlist mutation handle, distinct from
// ratingKey; smart playlists yield null playlistItemID). Refuses smart
// playlists.
func RemoveItem(playlistKey, playlistItemID string) (jsonx.J, *output.CLIError) {
	if isSmart(playlistKey) {
		return nil, smartContainerErr()
	}
	api.Delete(fmt.Sprintf("/playlists/%s/items/%s", playlistKey, playlistItemID), nil)
	return jsonx.J{"ok": true}, nil
}

// Clear mirrors playlists.clear: remove every item from a playlist. Refuses
// smart playlists.
func Clear(playlistKey string) (jsonx.J, *output.CLIError) {
	if isSmart(playlistKey) {
		return nil, smartContainerErr()
	}
	api.Delete(fmt.Sprintf("/playlists/%s/items", playlistKey), nil)
	return jsonx.J{"ok": true}, nil
}
