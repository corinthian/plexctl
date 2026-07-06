// Package sessions ports plexctl/sessions.py: now-playing, history,
// continue-watching, and the parallel-fetched context bundle.
package sessions

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/library"
	"github.com/corinthian/plexctl/internal/queuestate"
)

// historyParams builds the pagination + sort params shared by history() and
// _fetch_history_base(). PMS silently returns the *full* history unless both
// X-Plex-Container-Start and X-Plex-Container-Size are sent.
func historyParams(limit int) url.Values {
	return url.Values{
		"X-Plex-Container-Start": {"0"},
		"X-Plex-Container-Size":  {strconv.Itoa(limit)},
		"sort":                   {"viewedAt:desc"},
	}
}

// NowPlaying mirrors sessions.now_playing (idle is ok:true, state idle).
func NowPlaying(client jsonx.J) jsonx.J {
	machineID := client["machineIdentifier"]
	data := api.Get("/status/sessions", nil)
	items := jsonx.MapList(jsonx.GetMap(data, "MediaContainer"), "Metadata")
	for _, s := range items {
		player := jsonx.GetMap(s, "Player")
		if player["machineIdentifier"] == machineID {
			return jsonx.J{
				"ok":         true,
				"state":      player["state"],
				"title":      s["title"],
				"type":       s["type"],
				"show":       s["grandparentTitle"], // TV show name, nil for movies
				"season":     s["parentIndex"],      // season number
				"episode":    s["index"],            // episode number
				"year":       s["year"],             // movie year
				"viewOffset": s["viewOffset"],       // ms elapsed
				"duration":   s["duration"],         // ms total
				"ratingKey":  s["ratingKey"],
			}
		}
	}
	name, ok := client["name"]
	if !ok {
		name = machineID
	}
	return jsonx.J{"ok": true, "state": "idle", "client": name}
}

// CurrentRatingKey mirrors sessions.current_rating_key; "" when idle.
func CurrentRatingKey(client jsonx.J) string {
	rk, ok := NowPlaying(client)["ratingKey"]
	if !ok || rk == nil {
		return ""
	}
	return jsonx.AsStr(rk)
}

// History mirrors sessions.history.
func History(limit int) jsonx.J {
	data := api.Get("/status/sessions/history/all", historyParams(limit))
	entries := jsonx.MapList(jsonx.GetMap(data, "MediaContainer"), "Metadata")
	rows := make([]jsonx.J, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, jsonx.J{
			"title":     e["title"],
			"type":      e["type"],
			"show":      e["grandparentTitle"],
			"viewedAt":  e["viewedAt"],
			"ratingKey": e["ratingKey"],
			"year":      e["year"],
			"duration":  e["duration"],
		})
	}
	// The history endpoint omits duration/year — fill them per item (parallel).
	// Entries for deleted/moved media carry no ratingKey and stay nil (best-effort).
	library.FillDurations(rows)
	return jsonx.J{"ok": true, "history": rows}
}

// ContinueWatching mirrors sessions.continue_watching.
func ContinueWatching() jsonx.J {
	data := api.Get("/hubs/continueWatching", nil)
	hubs := jsonx.MapList(jsonx.GetMap(data, "MediaContainer"), "Hub")
	var items []jsonx.J
	for _, hub := range hubs {
		items = append(items, jsonx.MapList(hub, "Metadata")...)
	}
	rows := make([]jsonx.J, 0, len(items))
	for _, i := range items {
		rows = append(rows, jsonx.J{
			"title":      i["title"],
			"type":       i["type"],
			"show":       i["grandparentTitle"],
			"season":     i["parentIndex"],
			"episode":    i["index"],
			"viewOffset": i["viewOffset"],
			"duration":   i["duration"],
			"ratingKey":  i["ratingKey"],
		})
	}
	return jsonx.J{"ok": true, "items": rows}
}

// --- context: parallel bundle (unexported fetchers mirror the Python
// module-private _fetch_* / _history_row helpers) -----------------------------

func fetchNowPlaying(client jsonx.J) (jsonx.J, error) {
	machineID := client["machineIdentifier"]
	data, err := api.TryGet("/status/sessions", nil)
	if err != nil {
		return nil, err
	}
	items := jsonx.MapList(jsonx.GetMap(data, "MediaContainer"), "Metadata")
	for _, s := range items {
		player := jsonx.GetMap(s, "Player")
		if player["machineIdentifier"] == machineID {
			return jsonx.J{
				"state":      player["state"],
				"title":      s["title"],
				"type":       s["type"],
				"show":       s["grandparentTitle"],
				"season":     s["parentIndex"],
				"episode":    s["index"],
				"year":       s["year"],
				"viewOffset": s["viewOffset"],
				"duration":   s["duration"],
				"ratingKey":  s["ratingKey"],
			}, nil
		}
	}
	return jsonx.J{"state": "idle"}, nil
}

func fetchQueue(client jsonx.J) (jsonx.J, error) {
	mid, _ := client["machineIdentifier"].(string)
	var entry jsonx.J
	if mid != "" {
		entry = queuestate.Load(mid)
	}
	var qid any
	if entry != nil {
		qid = entry["playQueueID"]
	}
	if !jsonx.Truthy(qid) {
		return jsonx.J{"state": "empty", "playQueueID": nil, "selectedItemID": nil, "items": []jsonx.J{}}, nil
	}
	data, err := api.TryGet(fmt.Sprintf("/playQueues/%s", jsonx.AsStr(qid)), nil)
	if err != nil {
		// A stale saved id 404s once PMS prunes the queue. Drop the entry and
		// degrade to the empty-queue sub-object so a pruned queue never fails
		// the whole startup bundle (nowPlaying + history stay intact).
		var e *api.Error
		if errors.As(err, &e) && e.Status == 404 {
			if mid != "" {
				queuestate.Clear(mid)
			}
			return jsonx.J{"state": "empty", "playQueueID": nil, "selectedItemID": nil, "items": []jsonx.J{}}, nil
		}
		return nil, err
	}
	mc := jsonx.GetMap(data, "MediaContainer")
	selected := mc["playQueueSelectedItemID"]
	rawItems := jsonx.MapList(mc, "Metadata")
	items := make([]jsonx.J, 0, len(rawItems))
	for _, item := range rawItems {
		items = append(items, jsonx.J{
			"playQueueItemID": item["playQueueItemID"],
			"title":           item["title"],
			"type":            item["type"],
			"ratingKey":       item["ratingKey"],
			"duration":        item["duration"],
			"year":            item["year"],
			"selected":        item["playQueueItemID"] == selected,
		})
	}
	if len(items) == 0 {
		return jsonx.J{"state": "empty", "playQueueID": jsonx.AsStr(qid), "selectedItemID": nil, "items": []jsonx.J{}}, nil
	}
	return jsonx.J{"playQueueID": jsonx.AsStr(qid), "selectedItemID": selected, "items": items}, nil
}

func fetchHistoryBase(limit int) ([]jsonx.J, error) {
	data, err := api.TryGet("/status/sessions/history/all", historyParams(limit))
	if err != nil {
		return nil, err
	}
	return jsonx.MapList(jsonx.GetMap(data, "MediaContainer"), "Metadata"), nil
}

func fetchMetadata(ratingKey string) (jsonx.J, error) {
	data, err := api.TryGet(fmt.Sprintf("/library/metadata/%s", ratingKey), nil)
	if err != nil {
		return nil, err
	}
	items := jsonx.MapList(jsonx.GetMap(data, "MediaContainer"), "Metadata")
	if len(items) == 0 {
		return jsonx.J{}, nil
	}
	return items[0], nil
}

func historyRow(entry, meta jsonx.J) jsonx.J {
	if meta == nil {
		meta = jsonx.J{}
	}
	duration := entry["duration"]
	if !jsonx.Truthy(duration) {
		duration = meta["duration"]
	}
	year := entry["year"]
	if !jsonx.Truthy(year) {
		year = meta["year"]
	}
	return jsonx.J{
		"title":     entry["title"],
		"type":      entry["type"],
		"show":      entry["grandparentTitle"],
		"viewedAt":  entry["viewedAt"],
		"ratingKey": entry["ratingKey"],
		"duration":  duration,
		"year":      year,
	}
}

// failureSection renders a *api.Error (or any other error) as the {"ok":
// false, "error": ...} shape shared by every context() section.
func failureSection(err error) jsonx.J {
	var e *api.Error
	if !errors.As(err, &e) {
		e = &api.Error{Message: err.Error(), Kind: "error"}
	}
	return jsonx.J{"ok": false, "error": e.Message}
}

// Context mirrors sessions.context (goroutine-parallel fetch bundle).
//
// Three top-level fetches run concurrently; history rows fan out a metadata
// GET per item (also concurrently) to surface duration + year that the
// history endpoint omits. Per-section failures degrade gracefully — top-level
// ok is true as long as nowPlaying succeeded. Go's goroutines are cheap
// enough that (unlike the Python ThreadPoolExecutor) no worker-count cap is
// needed here.
func Context(client jsonx.J, historyLimit int, includeHistory bool) jsonx.J {
	started := time.Now()

	clientLabel := client["name"]
	if !jsonx.Truthy(clientLabel) {
		clientLabel = client["machineIdentifier"]
	}

	var wg sync.WaitGroup
	var npData jsonx.J
	var npErr error
	var qData jsonx.J
	var qErr error
	var hEntries []jsonx.J
	var hErr error

	wg.Add(2)
	go func() {
		defer wg.Done()
		npData, npErr = fetchNowPlaying(client)
	}()
	go func() {
		defer wg.Done()
		qData, qErr = fetchQueue(client)
	}()
	if includeHistory {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hEntries, hErr = fetchHistoryBase(historyLimit)
		}()
	}
	wg.Wait()

	var nowPlayingSection jsonx.J
	if npErr != nil {
		nowPlayingSection = failureSection(npErr)
	} else {
		nowPlayingSection = jsonx.J{"ok": true}
		for k, v := range npData {
			nowPlayingSection[k] = v
		}
	}

	var queueSection jsonx.J
	if qErr != nil {
		queueSection = failureSection(qErr)
	} else {
		queueSection = jsonx.J{"ok": true}
		for k, v := range qData {
			queueSection[k] = v
		}
	}

	var historySection jsonx.J
	if includeHistory {
		if hErr != nil {
			historySection = failureSection(hErr)
		} else {
			rows := make([]jsonx.J, len(hEntries))
			var rowsWG sync.WaitGroup
			for i, entry := range hEntries {
				rowsWG.Add(1)
				go func(i int, entry jsonx.J) {
					defer rowsWG.Done()
					rk := entry["ratingKey"]
					if !jsonx.Truthy(rk) {
						rows[i] = historyRow(entry, nil)
						return
					}
					meta, err := fetchMetadata(jsonx.AsStr(rk))
					if err != nil {
						rows[i] = historyRow(entry, nil)
						return
					}
					rows[i] = historyRow(entry, meta)
				}(i, entry)
			}
			rowsWG.Wait()
			historySection = jsonx.J{"ok": true, "items": rows}
		}
	}

	npOK, _ := nowPlayingSection["ok"].(bool)
	result := jsonx.J{
		"ok":         npOK,
		"client":     clientLabel,
		"nowPlaying": nowPlayingSection,
		"queue":      queueSection,
		"fetchedAt":  int(time.Now().Unix()),
		"elapsedMs":  int(time.Since(started).Milliseconds()),
	}
	if includeHistory {
		result["history"] = historySection
	}
	return result
}
