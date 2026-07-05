// Package streams ports plexctl/streams.py: the audio/subtitle domain —
// batched audit reads (chunked /library/metadata/{ids}) and track-selection
// writes (PUT /library/parts/{partId}), single + bulk with guards.
//
// Audit is a 2-call sequence (live-verified against PMS): /allLeaves gives the
// episode ratingKeys but the live server strips Stream[] from leaf listings,
// so a second batched GET /library/metadata/{ids} (comma-separated) returns
// the full Media -> Part -> Stream tree for every episode at once. Never one
// metadata call per episode.
package streams

import (
	"iter"
	"net/url"
	"strconv"
	"strings"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/library"
)

// Plex streamType discriminator on a Stream node.
const (
	Audio    = 2
	Subtitle = 3
)

// chunkSize is the batch size for the comma-separated metadata read. Keys are
// short; 100 keeps the URL well under any practical length limit.
const chunkSize = 100

// batchedMetadata fetches full metadata (Media -> Part -> Stream) for many
// ratingKeys via chunked batched GETs. Returns {ratingKey: metadata}.
//
// GET /library/metadata/{ids} takes a comma-separated id list and returns the
// full schema including streams — the call that makes the audit O(1) batches
// instead of O(N) per-episode fetches.
func batchedMetadata(ratingKeys []string) map[string]jsonx.J {
	out := map[string]jsonx.J{}
	for i := 0; i < len(ratingKeys); i += chunkSize {
		end := i + chunkSize
		if end > len(ratingKeys) {
			end = len(ratingKeys)
		}
		ids := strings.Join(ratingKeys[i:end], ",")
		resp := api.Get("/library/metadata/"+ids, nil)
		for _, m := range jsonx.MapList(jsonx.GetMap(resp, "MediaContainer"), "Metadata") {
			out[jsonx.AsStr(m["ratingKey"])] = m
		}
	}
	return out
}

// AudioStreams mirrors streams.audio_streams: all audio (streamType==2)
// streams across the item's Media -> Part tree.
func AudioStreams(meta jsonx.J) []jsonx.J {
	out := make([]jsonx.J, 0)
	for _, media := range jsonx.MapList(meta, "Media") {
		for _, part := range jsonx.MapList(media, "Part") {
			for _, s := range jsonx.MapList(part, "Stream") {
				if int(jsonx.Num(s["streamType"])) == Audio {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// strEq reports whether v is a string equal to target — mirrors Python's
// `d.get(k) == target`, which is false whenever the key is absent, null, or
// holds a non-string value.
func strEq(v any, target string) bool {
	s, ok := v.(string)
	return ok && s == target
}

func auditRow(e jsonx.J, byKey map[string]jsonx.J, preferred string) jsonx.J {
	key := jsonx.AsStr(e["ratingKey"])
	meta := byKey[key]
	if meta == nil {
		meta = jsonx.J{}
	}
	audio := AudioStreams(meta)

	var defaultStream, selected jsonx.J
	for _, s := range audio {
		if defaultStream == nil && jsonx.Truthy(s["default"]) {
			defaultStream = s
		}
		if selected == nil && jsonx.Truthy(s["selected"]) {
			selected = s
		}
	}

	row := jsonx.J{
		"ratingKey":          key,
		"season":             e["parentIndex"],
		"episode":            e["index"],
		"title":              e["title"],
		"defaultAudioLang":   nil,
		"defaultAudioCode":   nil,
		"selectedAudioCode":  nil,
		"hasEnglishAlt":      false,
		"isPreferredDefault": false,
	}
	if defaultStream != nil {
		row["defaultAudioLang"] = defaultStream["language"]
		row["defaultAudioCode"] = defaultStream["languageCode"]
		row["isPreferredDefault"] = strEq(defaultStream["languageCode"], preferred)
	}
	if selected != nil {
		row["selectedAudioCode"] = selected["languageCode"]
	}
	for _, s := range audio {
		if strEq(s["languageCode"], preferred) {
			row["hasEnglishAlt"] = true
			break
		}
	}
	return row
}

// IterAuditRows mirrors streams.iter_audit_rows: yield audit rows
// episode-by-episode, fetching metadata one chunk at a time.
//
// Rows for a chunk are yielded as soon as that chunk's batched GET returns,
// so a streaming caller (--ndjson) keeps partial progress if a later chunk
// fails. Total GET count is identical to the all-at-once path.
func IterAuditRows(episodes []jsonx.J, preferred string) iter.Seq[jsonx.J] {
	return func(yield func(jsonx.J) bool) {
		for i := 0; i < len(episodes); i += chunkSize {
			end := i + chunkSize
			if end > len(episodes) {
				end = len(episodes)
			}
			chunk := episodes[i:end]
			keys := make([]string, 0, len(chunk))
			for _, e := range chunk {
				if e["ratingKey"] != nil {
					keys = append(keys, jsonx.AsStr(e["ratingKey"]))
				}
			}
			byKey := batchedMetadata(keys)
			for _, e := range chunk {
				if !yield(auditRow(e, byKey, preferred)) {
					return
				}
			}
		}
	}
}

// AuditAudioForKey mirrors streams.audit_audio_for_key: per-episode audio
// audit for an already-resolved show ratingKey (generator).
func AuditAudioForKey(showKey any, preferred string, season *int) iter.Seq[jsonx.J] {
	return IterAuditRows(library.EpisodesForShowKey(showKey, false, season), preferred)
}

// --- write side: set the selected audio / subtitle track ---------------------
// PUT /library/parts/{partId}?audioStreamID=&subtitleStreamID=&allParts=1 sets
// which streams are *selected by this user* (not the file's `default`). There
// is no bulk endpoint — partId is singular; allParts=1 only spans the parts of
// the one item. Empty 200 body means success, so each function builds its own
// {"ok": true, ...} (never report the raw PUT response, which would read as
// failure).

// resolveTrack mirrors streams._resolve_track: find (partId, streamId) for
// the target track. (nil, nil) if absent.
//
// By explicit streamID (exact match) or by language (first track whose
// languageCode matches). partId is the Part carrying that stream. When
// streamID is set, language is ignored entirely (the if/elif in the Python
// original never reaches the language branch).
func resolveTrack(meta jsonx.J, streamType int, language string, streamID *int) (any, any) {
	for _, media := range jsonx.MapList(meta, "Media") {
		for _, part := range jsonx.MapList(media, "Part") {
			pid := part["id"]
			for _, s := range jsonx.MapList(part, "Stream") {
				if int(jsonx.Num(s["streamType"])) != streamType {
					continue
				}
				if streamID != nil {
					if jsonx.AsStr(s["id"]) == strconv.Itoa(*streamID) {
						return pid, s["id"]
					}
				} else if strEq(s["languageCode"], language) {
					return pid, s["id"]
				}
			}
		}
	}
	return nil, nil
}

// firstPartID mirrors streams._first_part_id.
func firstPartID(meta jsonx.J) any {
	for _, media := range jsonx.MapList(meta, "Media") {
		for _, part := range jsonx.MapList(media, "Part") {
			return part["id"]
		}
	}
	return nil
}

// SetAudioStream mirrors streams.set_audio_stream: select an audio track on
// one item, by language code or explicit stream id.
func SetAudioStream(ratingKey string, language string, streamID *int) jsonx.J {
	meta := library.Metadata(ratingKey)
	if !jsonx.Truthy(meta) {
		return jsonx.J{"ok": false, "error": "no metadata for ratingKey " + ratingKey}
	}
	partID, sid := resolveTrack(meta, Audio, language, streamID)
	if partID == nil {
		target := language + " audio"
		if streamID != nil {
			target = "audio stream id " + strconv.Itoa(*streamID)
		}
		return jsonx.J{"ok": false, "error": "no " + target + " track on " + ratingKey}
	}
	api.Put("/library/parts/"+jsonx.AsStr(partID), url.Values{
		"audioStreamID": {jsonx.AsStr(sid)},
		"allParts":      {"1"},
	})
	return jsonx.J{"ok": true, "ratingKey": jsonx.AsStr(ratingKey), "partId": partID, "audioStreamID": sid}
}

// SetSubtitleStream mirrors streams.set_subtitle_stream: select a subtitle
// track on one item, or disable subtitles (subtitleStreamID=0).
func SetSubtitleStream(ratingKey string, language string, streamID *int, disable bool) jsonx.J {
	meta := library.Metadata(ratingKey)
	if !jsonx.Truthy(meta) {
		return jsonx.J{"ok": false, "error": "no metadata for ratingKey " + ratingKey}
	}
	if disable {
		partID := firstPartID(meta)
		if partID == nil {
			return jsonx.J{"ok": false, "error": "no media part on " + ratingKey}
		}
		api.Put("/library/parts/"+jsonx.AsStr(partID), url.Values{
			"subtitleStreamID": {"0"},
			"allParts":         {"1"},
		})
		return jsonx.J{"ok": true, "ratingKey": jsonx.AsStr(ratingKey), "partId": partID,
			"subtitleStreamID": 0, "disabled": true}
	}
	partID, sid := resolveTrack(meta, Subtitle, language, streamID)
	if partID == nil {
		target := language + " subtitle"
		if streamID != nil {
			target = "subtitle stream id " + strconv.Itoa(*streamID)
		}
		return jsonx.J{"ok": false, "error": "no " + target + " track on " + ratingKey}
	}
	api.Put("/library/parts/"+jsonx.AsStr(partID), url.Values{
		"subtitleStreamID": {jsonx.AsStr(sid)},
		"allParts":         {"1"},
	})
	return jsonx.J{"ok": true, "ratingKey": jsonx.AsStr(ratingKey), "partId": partID, "subtitleStreamID": sid}
}

// --- bulk: set audio across a whole show -------------------------------------
// N items = N PUTs (the API has no bulk stream setter). One batched metadata
// read resolves every target, then per-episode PUTs via TryPut so one failure
// doesn't abort the batch. The planner reuses resolveTrack so the
// (partId, streamId) pairing is identical to the single-item path — never
// first-part + flat-scan.

// audioPlanRow mirrors streams._audio_plan_row: one bulk-plan entry for an
// episode — the from->to change, or a skip reason.
func audioPlanRow(ep jsonx.J, meta jsonx.J, language string, onlyNonEng bool) jsonx.J {
	audio := AudioStreams(meta)
	var current jsonx.J
	for _, s := range audio {
		if jsonx.Truthy(s["selected"]) {
			current = s
			break
		}
	}
	if current == nil {
		for _, s := range audio {
			if jsonx.Truthy(s["default"]) {
				current = s
				break
			}
		}
	}
	var fromCode any
	if current != nil {
		fromCode = current["languageCode"]
	}

	partID, sid := resolveTrack(meta, Audio, language, nil)
	row := jsonx.J{
		"ratingKey":  jsonx.AsStr(ep["ratingKey"]),
		"season":     ep["parentIndex"],
		"episode":    ep["index"],
		"title":      ep["title"],
		"partId":     partID,
		"fromCode":   fromCode,
		"toCode":     language,
		"toStreamId": sid,
		"skip":       false,
		"reason":     nil,
	}
	if partID == nil {
		row["skip"] = true
		row["reason"] = "no " + language + " audio track"
	} else if onlyNonEng && current != nil && strEq(current["languageCode"], language) {
		row["skip"] = true
		row["reason"] = "already preferred"
	}
	return row
}

// PlanBulkAudio mirrors streams.plan_bulk_audio: build the per-episode bulk
// plan via the single batched metadata read.
func PlanBulkAudio(episodes []jsonx.J, language string, onlyNonEng bool) []jsonx.J {
	keys := make([]string, 0, len(episodes))
	for _, e := range episodes {
		if e["ratingKey"] != nil {
			keys = append(keys, jsonx.AsStr(e["ratingKey"]))
		}
	}
	byKey := batchedMetadata(keys)
	rows := make([]jsonx.J, 0, len(episodes))
	for _, e := range episodes {
		meta := byKey[jsonx.AsStr(e["ratingKey"])]
		if meta == nil {
			meta = jsonx.J{}
		}
		rows = append(rows, audioPlanRow(e, meta, language, onlyNonEng))
	}
	return rows
}

func copyRow(row jsonx.J) jsonx.J {
	out := make(jsonx.J, len(row)+2)
	for k, v := range row {
		out[k] = v
	}
	return out
}

// ExecuteBulkAudio mirrors streams.execute_bulk_audio: PUT each non-skipped
// plan row via TryPut; tolerate per-item failure.
func ExecuteBulkAudio(plan []jsonx.J) []jsonx.J {
	results := make([]jsonx.J, 0, len(plan))
	for _, row := range plan {
		out := copyRow(row)
		if jsonx.Truthy(row["skip"]) {
			out["status"] = "skipped"
			results = append(results, out)
			continue
		}
		_, err := api.TryPut("/library/parts/"+jsonx.AsStr(row["partId"]), url.Values{
			"audioStreamID": {jsonx.AsStr(row["toStreamId"])},
			"allParts":      {"1"},
		})
		if err != nil {
			out["status"] = "error"
			out["error"] = err.Error() // *api.Error.Error() returns e.Message
		} else {
			out["status"] = "ok"
		}
		results = append(results, out)
	}
	return results
}
