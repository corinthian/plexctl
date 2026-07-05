// Package collections ports plexctl/collections.py: list/show with smart
// resolution, and manual-collection mutation with the smart guard, DELETE
// remove quirk, and title.locked rename.
//
// PMS exposes collections under each library section. A movie or show
// section can hold any number of curated collections; each collection has
// its own ratingKey and children. Smart collections are query-driven and
// reject mutation; callers are expected to check the `smart` flag from
// ListAll before issuing edits.
//
// PMS treats collections as metadata items: delete and rename go through
// /library/metadata/{key}, not /library/collections/{key}. Items inside a
// collection are addressed by their own ratingKey, not a separate
// collection-item-ID.
package collections

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/library"
	"github.com/corinthian/plexctl/internal/playback"
)

var videoSectionTypes = map[string]bool{"movie": true, "show": true}

var sectionTypeCodes = map[string]int{"movie": 1, "show": 2}

const smartRefusal = "smart collection: contents are query-driven and cannot be edited via " +
	"the API — edit the smart filter in the Plex app instead"

// isSmart is a best-effort smart-collection check. Returns false on lookup
// failure so the real mutation surfaces the canonical error rather than this
// probe.
//
// PMS 1.43 silently 2xx-no-ops mutations against smart collections (verified
// live 2026-05-13). Without a pre-flight guard, the JSON-only `ok: true`
// contract would lie to callers. This guard restores honesty.
func isSmart(ratingKey string) bool {
	r, err := api.TryGet(fmt.Sprintf("/library/metadata/%s", ratingKey), nil)
	if err != nil {
		return false
	}
	items := jsonx.MapList(jsonx.GetMap(r, "MediaContainer"), "Metadata")
	if len(items) == 0 {
		return false
	}
	return jsonx.Truthy(items[0]["smart"])
}

func uriFor(serverID, ratingKey string) string {
	return fmt.Sprintf("server://%s/com.plexapp.plugins.library/library/metadata/%s", serverID, ratingKey)
}

// resolveSectionTypeCode mirrors _resolve_section_type_code. Returns 0 when
// the section is unknown or is not a movie/show section.
func resolveSectionTypeCode(sectionID string) int {
	for _, s := range library.Sections() {
		if jsonx.AsStr(s["key"]) == sectionID {
			return sectionTypeCodes[jsonx.AsStr(s["type"])]
		}
	}
	return 0
}

// collectionRow mirrors _collection_row. Both call sites in ListAll always
// have a section id in hand, so sectionID is always included (unlike the
// Python default-None signature, which is never exercised with None here).
func collectionRow(item jsonx.J, sectionID string) jsonx.J {
	return jsonx.J{
		"ratingKey":  item["ratingKey"],
		"title":      item["title"],
		"childCount": item["childCount"],
		"subtype":    item["subtype"],
		"smart":      jsonx.Truthy(item["smart"]),
		"sectionID":  sectionID,
	}
}

// ListAll mirrors collections.list_all. sectionID == "" means every video
// section.
func ListAll(sectionID string) []jsonx.J {
	if sectionID != "" {
		resp := api.Get(fmt.Sprintf("/library/sections/%s/collections", sectionID), nil)
		items := jsonx.MapList(jsonx.GetMap(resp, "MediaContainer"), "Metadata")
		rows := make([]jsonx.J, 0, len(items))
		for _, i := range items {
			rows = append(rows, collectionRow(i, sectionID))
		}
		return rows
	}

	results := make([]jsonx.J, 0)
	for _, s := range library.Sections() {
		t, _ := s["type"].(string)
		if !videoSectionTypes[t] {
			continue
		}
		sid := s["key"]
		if !jsonx.Truthy(sid) {
			continue
		}
		sidStr := jsonx.AsStr(sid)
		resp := api.Get(fmt.Sprintf("/library/sections/%s/collections", sidStr), nil)
		items := jsonx.MapList(jsonx.GetMap(resp, "MediaContainer"), "Metadata")
		for _, i := range items {
			results = append(results, collectionRow(i, sidStr))
		}
	}
	return results
}

const smartContentMarker = "/com.plexapp.plugins.library"

// smartContentPath extracts the section-query path from a smart collection's
// `content` URI. Smart collections store their filter as e.g.
// `server://<id>/com.plexapp.plugins.library/library/sections/1/all?type=1&...`.
// The portion after the marker is a path-with-query the server will evaluate
// against the live library. Returns "" when the marker is absent or the
// input is falsy.
func smartContentPath(content any) string {
	if !jsonx.Truthy(content) {
		return ""
	}
	s, ok := content.(string)
	if !ok {
		return ""
	}
	idx := strings.Index(s, smartContentMarker)
	if idx < 0 {
		return ""
	}
	return s[idx+len(smartContentMarker):]
}

// formatItem mirrors _format_item. viewCount defaults to 0 only when the key
// is absent (Python `i.get("viewCount", 0)`), not when it is present as null.
func formatItem(i jsonx.J) jsonx.J {
	viewCount, ok := i["viewCount"]
	if !ok {
		viewCount = 0
	}
	return jsonx.J{
		"ratingKey": i["ratingKey"],
		"title":     i["title"],
		"type":      i["type"],
		"year":      i["year"],
		"duration":  i["duration"],
		"viewCount": viewCount,
	}
}

// Show mirrors collections.show (smart-filter resolution unless raw).
//
// For smart collections, PMS's /children endpoint returns a stale (usually
// empty) cache instead of re-evaluating the rule. We work around this by
// reading the collection's metadata, extracting the smart-filter URI from
// the `content` field, and GETting that path directly. Manual collections
// pay one extra metadata round trip but otherwise behave identically.
//
// Pass raw=true to bypass smart resolution and return PMS's raw /children
// payload — useful only for debugging.
func Show(ratingKey string, raw bool) []jsonx.J {
	if !raw {
		meta, err := api.TryGet(fmt.Sprintf("/library/metadata/%s", ratingKey), nil)
		if err != nil {
			meta = nil
		}
		items := jsonx.MapList(jsonx.GetMap(meta, "MediaContainer"), "Metadata")
		record := jsonx.J{}
		if len(items) > 0 {
			record = items[0]
		}
		if jsonx.Truthy(record["smart"]) {
			path := smartContentPath(record["content"])
			if path != "" {
				resp := api.Get(path, nil)
				live := jsonx.MapList(jsonx.GetMap(resp, "MediaContainer"), "Metadata")
				rows := make([]jsonx.J, 0, len(live))
				for _, i := range live {
					rows = append(rows, formatItem(i))
				}
				// /sections/all carries duration -> fill_durations is a no-op here.
				return library.FillDurations(rows)
			}
		}
	}

	resp := api.Get(fmt.Sprintf("/library/metadata/%s/children", ratingKey), nil)
	items := jsonx.MapList(jsonx.GetMap(resp, "MediaContainer"), "Metadata")
	rows := make([]jsonx.J, 0, len(items))
	for _, i := range items {
		rows = append(rows, formatItem(i))
	}
	// /children omits duration -> fill it per item (parallel).
	return library.FillDurations(rows)
}

// --- mutations ---------------------------------------------------------------

// Create mirrors collections.create: create a manual collection seeded with
// ratingKeys.
//
// Smart collections are not supported. Section type is auto-resolved from
// library.Sections(); we only know how to map movie + show sections to PMS
// type codes today.
func Create(title, sectionID string, ratingKeys []string) jsonx.J {
	if len(ratingKeys) == 0 {
		return jsonx.J{"ok": false, "error": "create requires at least one ratingKey"}
	}
	serverID := playback.GetServerMachineID()
	if serverID == "" {
		return jsonx.J{"ok": false, "error": "could not retrieve server machineIdentifier"}
	}
	typeCode := resolveSectionTypeCode(sectionID)
	if typeCode == 0 {
		return jsonx.J{"ok": false, "error": fmt.Sprintf("section %s is not a movie or show section", sectionID)}
	}

	data := api.Post("/library/collections", url.Values{
		"title":     {title},
		"sectionId": {sectionID},
		"type":      {strconv.Itoa(typeCode)},
		"smart":     {"0"},
		"uri":       {uriFor(serverID, ratingKeys[0])},
	})
	items := jsonx.MapList(jsonx.GetMap(data, "MediaContainer"), "Metadata")
	if len(items) == 0 {
		return jsonx.J{"ok": false, "error": "collection creation returned no metadata"}
	}
	collectionKey := items[0]["ratingKey"]
	if !jsonx.Truthy(collectionKey) {
		return jsonx.J{"ok": false, "error": "collection creation returned no ratingKey"}
	}
	key := jsonx.AsStr(collectionKey)

	for _, rk := range ratingKeys[1:] {
		r := addItemsFn(key, []string{rk}, serverID, true)
		if !jsonx.Truthy(r["ok"]) {
			_, _ = api.TryDelete(fmt.Sprintf("/library/metadata/%s", key), nil)
			r["partialCollectionID"] = key
			r["rollbackAttempted"] = true
			return r
		}
	}

	return jsonx.J{"ok": true, "ratingKey": key, "title": title, "count": len(ratingKeys)}
}

// Delete mirrors collections.delete. Collections are metadata items in PMS.
// Allowed on smart collections — deletion is unambiguous.
func Delete(collectionKey string) jsonx.J {
	api.Delete(fmt.Sprintf("/library/metadata/%s", collectionKey), nil)
	return jsonx.J{"ok": true}
}

// Rename mirrors collections.rename. Locks the title so the scanner won't
// override it. Refuses smart collections — see isSmart for the rationale.
func Rename(collectionKey, newTitle string) jsonx.J {
	if isSmart(collectionKey) {
		return jsonx.J{"ok": false, "error": smartRefusal}
	}
	api.Put(fmt.Sprintf("/library/metadata/%s", collectionKey), url.Values{
		"title.value":  {newTitle},
		"title.locked": {"1"},
	})
	return jsonx.J{"ok": true}
}

// addItemsFn is a seam so Create's mid-loop rollback path can be exercised
// without a real HTTP failure: with trustManual=true, a resolved serverID,
// and a single-key slice, addItemsInternal has no real failure path (api.Put
// print-and-exits rather than returning ok:false), mirroring the Python test
// suite's monkeypatch of add_items itself. Production always calls through
// addItemsInternal unchanged.
var addItemsFn = addItemsInternal

func addItemsInternal(collectionKey string, ratingKeys []string, serverID string, trustManual bool) jsonx.J {
	if len(ratingKeys) == 0 {
		return jsonx.J{"ok": false, "error": "add requires at least one ratingKey"}
	}
	if !trustManual && isSmart(collectionKey) {
		return jsonx.J{"ok": false, "error": smartRefusal}
	}
	sid := serverID
	if sid == "" {
		sid = playback.GetServerMachineID()
	}
	if sid == "" {
		return jsonx.J{"ok": false, "error": "could not retrieve server machineIdentifier"}
	}
	for _, rk := range ratingKeys {
		api.Put(fmt.Sprintf("/library/collections/%s/items", collectionKey), url.Values{"uri": {uriFor(sid, rk)}})
	}
	return jsonx.J{"ok": true, "added": len(ratingKeys)}
}

// AddItems mirrors collections.add_items (public entry point: no server-id
// override, no trust of the smart guard).
func AddItems(collectionKey string, ratingKeys []string) jsonx.J {
	return addItemsInternal(collectionKey, ratingKeys, "", false)
}

// RemoveItem mirrors collections.remove_item: remove an item from a
// collection by its ratingKey.
//
// The OpenAPI 3.1.0 spec (PMS 1.2.0) documents this as PUT, but PMS 1.43
// returns HTTP 404 on PUT and accepts DELETE — live-verified 2026-05-13
// against PMS 1.43.0. We use DELETE. Refuses smart collections.
func RemoveItem(collectionKey, itemRatingKey string) jsonx.J {
	if isSmart(collectionKey) {
		return jsonx.J{"ok": false, "error": smartRefusal}
	}
	api.Delete(fmt.Sprintf("/library/collections/%s/items/%s", collectionKey, itemRatingKey), nil)
	return jsonx.J{"ok": true}
}
