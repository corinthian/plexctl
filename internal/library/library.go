// Package library ports plexctl/library.py: search, sections, section
// listing with the client-side show unwatched filter, metadata, episode
// enumeration, scrobble/rate.
package library

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"sync"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/jsonx"
)

var typeMap = map[string]int{"show": 2, "movie": 1, "episode": 4}

var sectionTypeMap = map[string]int{"show": 2, "movie": 1}

func extractMetadata(hubResponse jsonx.J, typeFilter string) []jsonx.J {
	hubs := jsonx.MapList(jsonx.GetMap(hubResponse, "MediaContainer"), "Hub")
	results := make([]jsonx.J, 0)
	for _, hub := range hubs {
		if typeFilter != "" {
			t, _ := hub["type"].(string)
			if t != typeFilter {
				continue
			}
		}
		results = append(results, jsonx.MapList(hub, "Metadata")...)
	}
	return results
}

// itemScore mirrors _item_score: Plex returns `score` as a string like "1.5";
// coerce safely. Missing/falsy/unparseable → 0.0.
func itemScore(item jsonx.J) float64 {
	v, ok := item["score"]
	if !ok || !jsonx.Truthy(v) {
		return 0.0
	}
	switch t := v.(type) {
	case string:
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return 0.0
		}
		return f
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return 0.0
		}
		return f
	case bool:
		if t {
			return 1.0
		}
		return 0.0
	}
	return 0.0
}

func filterByScore(items []jsonx.J, minScore float64) []jsonx.J {
	if minScore <= 0 {
		return items
	}
	out := make([]jsonx.J, 0, len(items))
	for _, i := range items {
		if itemScore(i) >= minScore {
			out = append(out, i)
		}
	}
	return out
}

// Search mirrors library.search. mediaType == "" means unfiltered.
func Search(query, mediaType string, minScore float64) []jsonx.J {
	if resp, err := api.TryGet("/hubs/search/voice", url.Values{"query": {query}}); err == nil {
		results := filterByScore(extractMetadata(resp, mediaType), minScore)
		if len(results) > 0 {
			return results
		}
	}
	// voice hub failed or came back empty after the score filter — fall back
	// to the plain hub search.
	params := url.Values{"query": {query}, "limit": {"10"}}
	if code, ok := typeMap[mediaType]; ok {
		params.Set("type", strconv.Itoa(code))
	}
	resp := api.Get("/hubs/search", params)
	return filterByScore(extractMetadata(resp, mediaType), minScore)
}

// ResolveShow mirrors library.resolve_show; nil when no hit.
//
// Disables the score filter — we only want the top hit, and a niche show may
// score below the default threshold.
func ResolveShow(showQuery string) jsonx.J {
	hits := Search(showQuery, "show", 0)
	if len(hits) == 0 {
		return nil
	}
	return hits[0]
}

// ShowEpisodes mirrors library.show_episodes. season == nil means all seasons.
func ShowEpisodes(showQuery string, unwatchedOnly bool, season *int) []jsonx.J {
	hit := ResolveShow(showQuery)
	if hit == nil {
		return []jsonx.J{}
	}
	return EpisodesForShowKey(hit["ratingKey"], unwatchedOnly, season)
}

// EpisodesForShowKey mirrors library.episodes_for_show_key. showKey may be a
// string (from the CLI) or a float64 (a PMS ratingKey passed straight through
// JSON) — render with jsonx.AsStr.
func EpisodesForShowKey(showKey any, unwatchedOnly bool, season *int) []jsonx.J {
	resp := api.Get(fmt.Sprintf("/library/metadata/%s/allLeaves", jsonx.AsStr(showKey)), nil)
	episodes := jsonx.MapList(jsonx.GetMap(resp, "MediaContainer"), "Metadata")

	sort.SliceStable(episodes, func(i, j int) bool {
		pi, pj := jsonx.Num(episodes[i]["parentIndex"]), jsonx.Num(episodes[j]["parentIndex"])
		if pi != pj {
			return pi < pj
		}
		return jsonx.Num(episodes[i]["index"]) < jsonx.Num(episodes[j]["index"])
	})

	if season != nil {
		filtered := make([]jsonx.J, 0, len(episodes))
		for _, e := range episodes {
			if jsonx.Num(e["parentIndex"]) == float64(*season) {
				filtered = append(filtered, e)
			}
		}
		episodes = filtered
	}
	if unwatchedOnly {
		filtered := make([]jsonx.J, 0, len(episodes))
		for _, e := range episodes {
			if !jsonx.Truthy(e["viewCount"]) {
				filtered = append(filtered, e)
			}
		}
		episodes = filtered
	}
	return episodes
}

func airedAt(e jsonx.J) string {
	v, ok := e["originallyAvailableAt"]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return jsonx.AsStr(v)
}

// LatestUnwatchedEpisode mirrors library.latest_unwatched_episode; nil when
// no show matches (or strict and nothing unwatched).
//
// Shares only the enumeration with ShowEpisodes; the non-strict, no-unwatched
// fallback (latest *aired*, not the pilot) lives here.
func LatestUnwatchedEpisode(showQuery string, strict bool) jsonx.J {
	episodes := ShowEpisodes(showQuery, false, nil)
	if len(episodes) == 0 {
		return nil
	}

	for _, e := range episodes {
		if !jsonx.Truthy(e["viewCount"]) {
			return e
		}
	}

	if strict {
		return nil
	}

	// Python's max() keeps the first maximal element on ties — scan left to
	// right, replace only on strictly greater.
	best := episodes[0]
	bestAired := airedAt(best)
	for _, e := range episodes[1:] {
		aired := airedAt(e)
		if aired > bestAired {
			best = e
			bestAired = aired
		}
	}
	return best
}

// Sections mirrors library.sections.
func Sections() []jsonx.J {
	resp := api.Get("/library/sections", nil)
	dirs := jsonx.MapList(jsonx.GetMap(resp, "MediaContainer"), "Directory")
	rows := make([]jsonx.J, 0, len(dirs))
	for _, d := range dirs {
		rows = append(rows, jsonx.J{"key": d["key"], "title": d["title"], "type": d["type"]})
	}
	return rows
}

// hasUnwatched mirrors _has_unwatched: true when item has anything left to
// watch. Shows are judged by their leaf counters — never trust PMS's
// server-side unwatched=1 filter for shows, it keys on the show object's
// play-history viewCount, not episode watch state, so it both returns
// fully-watched shows (never "played") and drops unwatched shows that have
// play history. Movies are leaves — plain viewCount == 0 is correct for them.
func hasUnwatched(item jsonx.J) bool {
	if t, _ := item["type"].(string); t == "show" {
		return jsonx.Num(item["viewedLeafCount"]) < jsonx.Num(item["leafCount"])
	}
	return !jsonx.Truthy(item["viewCount"])
}

func sectionRow(i jsonx.J) jsonx.J {
	viewCount, ok := i["viewCount"]
	if !ok {
		viewCount = 0
	}
	row := jsonx.J{
		"ratingKey": i["ratingKey"],
		"title":     i["title"],
		"type":      i["type"],
		"year":      i["year"],
		"duration":  i["duration"],
		"viewCount": viewCount,
		"addedAt":   i["addedAt"],
	}
	if t, _ := i["type"].(string); t == "show" {
		// Show-level viewCount is a play counter, useless as a watched
		// signal — expose the leaf counters so callers can render real
		// watch state.
		leaf := int(jsonx.Num(i["leafCount"]))
		viewed := int(jsonx.Num(i["viewedLeafCount"]))
		row["leafCount"] = leaf
		row["viewedLeafCount"] = viewed
		unwatchedLeaves := leaf - viewed
		if unwatchedLeaves < 0 {
			unwatchedLeaves = 0
		}
		row["unwatchedLeaves"] = unwatchedLeaves
	}
	return row
}

// ListSection mirrors library.list_section. mediaType/sort == "" mean unset.
func ListSection(sectionID, mediaType string, unwatched bool, sort string) []jsonx.J {
	params := url.Values{}
	if code, ok := sectionTypeMap[mediaType]; ok {
		params.Set("type", strconv.Itoa(code))
	}
	if unwatched && mediaType == "movie" {
		// Server-side filter is trustworthy only for leaf items; shows are
		// filtered client-side in hasUnwatched (see its doc comment).
		params.Set("unwatched", "1")
	}
	if sort != "" {
		params.Set("sort", sort)
	}
	resp := api.Get(fmt.Sprintf("/library/sections/%s/all", sectionID), params)
	items := jsonx.MapList(jsonx.GetMap(resp, "MediaContainer"), "Metadata")
	if unwatched {
		filtered := make([]jsonx.J, 0, len(items))
		for _, i := range items {
			if hasUnwatched(i) {
				filtered = append(filtered, i)
			}
		}
		items = filtered
	}
	rows := make([]jsonx.J, 0, len(items))
	for _, i := range items {
		rows = append(rows, sectionRow(i))
	}
	return rows
}

// Metadata mirrors library.metadata; empty J when not found.
func Metadata(ratingKey string) jsonx.J {
	resp := api.Get(fmt.Sprintf("/library/metadata/%s", ratingKey), nil)
	items := jsonx.MapList(jsonx.GetMap(resp, "MediaContainer"), "Metadata")
	if len(items) == 0 {
		return jsonx.J{}
	}
	return items[0]
}

// safeMetadata mirrors _safe_metadata: best-effort single-item metadata
// fetch — returns {} on any failure.
func safeMetadata(ratingKey string) jsonx.J {
	resp, err := api.TryGet(fmt.Sprintf("/library/metadata/%s", ratingKey), nil)
	if err != nil {
		return jsonx.J{}
	}
	items := jsonx.MapList(jsonx.GetMap(resp, "MediaContainer"), "Metadata")
	if len(items) == 0 {
		return jsonx.J{}
	}
	return items[0]
}

// FillDurations mirrors library.fill_durations (parallel best-effort fill of
// duration/year on rows missing them). Mutates rows in place and returns them.
//
// Rows that already carry a non-nil duration are left untouched, so this is a
// no-op when the source response already supplied it. Best-effort: a per-item
// failure leaves that row unchanged.
func FillDurations(rows []jsonx.J) []jsonx.J {
	var targets []jsonx.J
	for _, r := range rows {
		if r["duration"] == nil && jsonx.Truthy(r["ratingKey"]) {
			targets = append(targets, r)
		}
	}
	if len(targets) == 0 {
		return rows
	}

	workers := len(targets)
	if workers > 8 {
		workers = 8
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, r := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(row jsonx.J) {
			defer wg.Done()
			defer func() { <-sem }()
			meta := safeMetadata(jsonx.AsStr(row["ratingKey"]))
			if len(meta) == 0 {
				return
			}
			if row["duration"] == nil {
				row["duration"] = meta["duration"]
			}
			if row["year"] == nil {
				row["year"] = meta["year"]
			}
		}(r)
	}
	wg.Wait()
	return rows
}

// Scrobble mirrors library.scrobble.
func Scrobble(ratingKey string) jsonx.J {
	api.Get("/:/scrobble", url.Values{"key": {ratingKey}, "identifier": {"com.plexapp.plugins.library"}})
	return jsonx.J{"ok": true}
}

// Unscrobble mirrors library.unscrobble.
func Unscrobble(ratingKey string) jsonx.J {
	api.Get("/:/unscrobble", url.Values{"key": {ratingKey}, "identifier": {"com.plexapp.plugins.library"}})
	return jsonx.J{"ok": true}
}

// Rate mirrors library.rate.
func Rate(ratingKey string, rating int) jsonx.J {
	api.Get("/:/rate", url.Values{
		"key":        {ratingKey},
		"identifier": {"com.plexapp.plugins.library"},
		"rating":     {strconv.Itoa(rating)},
	})
	return jsonx.J{"ok": true}
}
