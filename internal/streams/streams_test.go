package streams_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/streams"
	"github.com/corinthian/plexctl/internal/testutil"
)

// --- fake PMS harness --------------------------------------------------------

type capturedReq struct {
	method string
	path   string
	query  url.Values
}

type fakePMS struct {
	mu        sync.Mutex
	calls     []capturedReq
	metaByKey map[string]jsonx.J
	leaves    []jsonx.J
	putQueue  []int // HTTP statuses consumed FIFO by successive PUT /library/parts/*; default 200
}

func newFakePMS(t *testing.T) *fakePMS {
	t.Helper()
	f := &fakePMS{metaByKey: map[string]jsonx.J{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, capturedReq{method: r.Method, path: r.URL.Path, query: r.URL.Query()})
		f.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/allLeaves"):
			f.mu.Lock()
			body := leavesResp(f.leaves...)
			f.mu.Unlock()
			writeJSON(w, 200, body)

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/library/metadata/"):
			idsPart := strings.TrimPrefix(r.URL.Path, "/library/metadata/")
			ids := strings.Split(idsPart, ",")
			f.mu.Lock()
			items := make([]jsonx.J, 0, len(ids))
			for _, id := range ids {
				if m, ok := f.metaByKey[id]; ok {
					items = append(items, m)
				}
			}
			f.mu.Unlock()
			writeJSON(w, 200, metaResp(items...))

		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/library/parts/"):
			status := 200
			f.mu.Lock()
			if len(f.putQueue) > 0 {
				status = f.putQueue[0]
				f.putQueue = f.putQueue[1:]
			}
			f.mu.Unlock()
			if status >= 400 {
				http.Error(w, "boom", status)
				return
			}
			w.WriteHeader(status) // empty 200 body — PMS write-success signaling trap

		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 404)
		}
	}))
	t.Cleanup(srv.Close)
	testutil.Setup(t, srv.URL)
	return f
}

func (f *fakePMS) addMeta(ratingKey string, item jsonx.J) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metaByKey[ratingKey] = item
}

func (f *fakePMS) setLeaves(items ...jsonx.J) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leaves = items
}

func (f *fakePMS) enqueuePutStatus(statuses ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putQueue = append(f.putQueue, statuses...)
}

func (f *fakePMS) callsWhere(pred func(capturedReq) bool) []capturedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []capturedReq
	for _, c := range f.calls {
		if pred(c) {
			out = append(out, c)
		}
	}
	return out
}

func isMetaGET(c capturedReq) bool {
	return c.method == http.MethodGet && strings.HasPrefix(c.path, "/library/metadata/") && !strings.HasSuffix(c.path, "/allLeaves")
}

func isPartsPUT(c capturedReq) bool {
	return c.method == http.MethodPut && strings.HasPrefix(c.path, "/library/parts/")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// --- fixture builders --------------------------------------------------------

func anyList(items ...jsonx.J) []any {
	out := make([]any, len(items))
	for i, it := range items {
		out[i] = it
	}
	return out
}

func metaResp(items ...jsonx.J) jsonx.J {
	return jsonx.J{"MediaContainer": jsonx.J{"Metadata": anyList(items...)}}
}

func leavesResp(items ...jsonx.J) jsonx.J {
	return jsonx.J{"MediaContainer": jsonx.J{"Metadata": anyList(items...)}}
}

func ep(season, index int, key string) jsonx.J {
	return jsonx.J{
		"ratingKey":   key,
		"parentIndex": float64(season),
		"index":       float64(index),
		"title":       fmt.Sprintf("S%dE%d", season, index),
	}
}

// audioStream builds a streamType==Audio node. lang == "" defaults language
// to code, mirroring the Python tests' _stream(..., lang=None) helper.
func audioStream(code string, id any, isDefault, selected bool, lang string) jsonx.J {
	if lang == "" {
		lang = code
	}
	return jsonx.J{
		"streamType":   float64(streams.Audio),
		"languageCode": code,
		"language":     lang,
		"id":           id,
		"default":      isDefault,
		"selected":     selected,
	}
}

func subtitleStream(code string, id any) jsonx.J {
	return jsonx.J{
		"streamType":   float64(streams.Subtitle),
		"languageCode": code,
		"language":     code,
		"id":           id,
		"default":      false,
		"selected":     false,
	}
}

func part(id any, strms ...jsonx.J) jsonx.J {
	return jsonx.J{"id": id, "Stream": anyList(strms...)}
}

func metaWithParts(ratingKey string, parts ...jsonx.J) jsonx.J {
	return jsonx.J{"ratingKey": ratingKey, "Media": []any{jsonx.J{"Part": anyList(parts...)}}}
}

func meta(ratingKey string, partID any, strms ...jsonx.J) jsonx.J {
	return metaWithParts(ratingKey, part(partID, strms...))
}

func intPtr(v int) *int { return &v }

func collect(seq func(func(jsonx.J) bool)) []jsonx.J {
	var out []jsonx.J
	seq(func(j jsonx.J) bool {
		out = append(out, j)
		return true
	})
	return out
}

// --- AudioStreams: pure function, no network ---------------------------------

func TestAudioStreamsFiltersToAudioOnly(t *testing.T) {
	m := meta("100", 1100,
		audioStream("eng", 1, true, false, ""),
		audioStream("jpn", 2, false, false, ""),
		subtitleStream("eng", 3),
	)
	got := streams.AudioStreams(m)
	if len(got) != 2 || got[0]["languageCode"] != "eng" || got[1]["languageCode"] != "jpn" {
		t.Fatalf("AudioStreams = %#v, want [eng jpn] (subtitle dropped)", got)
	}
}

func TestAudioStreamsEmptyForEmptyMeta(t *testing.T) {
	if got := streams.AudioStreams(jsonx.J{}); len(got) != 0 {
		t.Fatalf("AudioStreams({}) = %#v, want empty", got)
	}
}

// --- IterAuditRows: default/selected reporting -------------------------------

func TestAuditReportsBothDefaultAndSelectedDistinct(t *testing.T) {
	f := newFakePMS(t)
	f.addMeta("100", meta("100", 1100,
		audioStream("deu", 1, true, false, "German"),
		audioStream("eng", 2, false, true, "English"),
	))
	episodes := []jsonx.J{ep(1, 1, "100")}

	rows := collect(streams.IterAuditRows(episodes, "eng"))
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want 1", rows)
	}
	r := rows[0]
	if r["defaultAudioCode"] != "deu" || r["defaultAudioLang"] != "German" {
		t.Fatalf("default fields = %#v, want deu/German", r)
	}
	if r["selectedAudioCode"] != "eng" {
		t.Fatalf("selectedAudioCode = %#v, want eng", r["selectedAudioCode"])
	}
	if r["isPreferredDefault"] != false {
		t.Fatalf("isPreferredDefault = %#v, want false (default is German)", r["isPreferredDefault"])
	}
	if r["hasEnglishAlt"] != true {
		t.Fatalf("hasEnglishAlt = %#v, want true (eng track exists)", r["hasEnglishAlt"])
	}
	if len(f.callsWhere(isMetaGET)) != 1 {
		t.Fatalf("metadata GET calls = %d, want 1", len(f.callsWhere(isMetaGET)))
	}
}

func TestAuditFlagsPreferredDefault(t *testing.T) {
	f := newFakePMS(t)
	f.addMeta("100", meta("100", 1100, audioStream("eng", 1, true, true, "")))
	rows := collect(streams.IterAuditRows([]jsonx.J{ep(1, 1, "100")}, "eng"))
	if rows[0]["isPreferredDefault"] != true {
		t.Fatalf("isPreferredDefault = %#v, want true", rows[0]["isPreferredDefault"])
	}
	if rows[0]["hasEnglishAlt"] != true {
		t.Fatalf("hasEnglishAlt = %#v, want true", rows[0]["hasEnglishAlt"])
	}
}

func TestAuditNonPreferredLanguageFlag(t *testing.T) {
	// --preferred jpn: an English-default episode is NOT the preferred default,
	// but a jpn alt does exist.
	f := newFakePMS(t)
	f.addMeta("100", meta("100", 1100,
		audioStream("eng", 1, true, false, ""),
		audioStream("jpn", 2, false, false, ""),
	))
	rows := collect(streams.IterAuditRows([]jsonx.J{ep(1, 1, "100")}, "jpn"))
	if rows[0]["isPreferredDefault"] != false {
		t.Fatalf("isPreferredDefault = %#v, want false", rows[0]["isPreferredDefault"])
	}
	if rows[0]["hasEnglishAlt"] != true {
		t.Fatalf("hasEnglishAlt = %#v, want true (jpn alt exists)", rows[0]["hasEnglishAlt"])
	}
}

func TestAuditEmptyWhenNoEpisodes(t *testing.T) {
	f := newFakePMS(t)
	rows := collect(streams.IterAuditRows(nil, "eng"))
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want empty", rows)
	}
	if n := len(f.callsWhere(isMetaGET)); n != 0 {
		t.Fatalf("metadata GET calls = %d, want 0 (no episodes -> no fetch)", n)
	}
}

func TestAuditRowMissingMetadataIsAllNil(t *testing.T) {
	newFakePMS(t) // no metadata registered for "999"
	rows := collect(streams.IterAuditRows([]jsonx.J{ep(3, 4, "999")}, "eng"))
	r := rows[0]
	for _, k := range []string{"defaultAudioLang", "defaultAudioCode", "selectedAudioCode"} {
		if r[k] != nil {
			t.Fatalf("row[%q] = %#v, want nil", k, r[k])
		}
	}
	if r["hasEnglishAlt"] != false || r["isPreferredDefault"] != false {
		t.Fatalf("row = %#v, want both flags false", r)
	}
	if r["ratingKey"] != "999" || r["season"] != float64(3) || r["episode"] != float64(4) {
		t.Fatalf("row passthrough fields wrong: %#v", r)
	}
}

// --- IterAuditRows: batching --------------------------------------------------

func TestAuditBatchesMetadataInChunks(t *testing.T) {
	const n = 101 // chunkSize(100) + 1
	f := newFakePMS(t)
	episodes := make([]jsonx.J, 0, n)
	for i := 0; i < n; i++ {
		key := strconv.Itoa(i)
		episodes = append(episodes, ep(1, i, key))
		f.addMeta(key, meta(key, 1000+i, audioStream("eng", 1, true, false, "")))
	}

	rows := collect(streams.IterAuditRows(episodes, "eng"))
	if len(rows) != n {
		t.Fatalf("rows = %d, want %d", len(rows), n)
	}
	calls := f.callsWhere(isMetaGET)
	if len(calls) != 2 {
		t.Fatalf("metadata GET calls = %d, want 2 (ceil(101/100))", len(calls))
	}
	counts := make([]int, len(calls))
	for i, c := range calls {
		idsPart := strings.TrimPrefix(c.path, "/library/metadata/")
		counts[i] = len(strings.Split(idsPart, ","))
	}
	if counts[0] != 100 || counts[1] != 1 {
		t.Fatalf("per-call id counts = %v, want [100 1]", counts)
	}
}

func TestIterAuditRowsStreamingStopsAfterFirstChunk(t *testing.T) {
	const n = 150 // two chunks: 100 + 50
	f := newFakePMS(t)
	episodes := make([]jsonx.J, 0, n)
	for i := 0; i < n; i++ {
		key := strconv.Itoa(i)
		episodes = append(episodes, ep(1, i, key))
		f.addMeta(key, meta(key, 1000+i, audioStream("eng", 1, true, false, "")))
	}

	seen := 0
	for row := range streams.IterAuditRows(episodes, "eng") {
		_ = row
		seen++
		break // consume exactly one row, then stop iterating
	}
	if seen != 1 {
		t.Fatalf("seen = %d, want 1", seen)
	}
	if n := len(f.callsWhere(isMetaGET)); n != 1 {
		t.Fatalf("metadata GET calls after early break = %d, want 1 (second chunk must not fetch)", n)
	}
}

// --- AuditAudioForKey: composition with library.EpisodesForShowKey -----------

func TestAuditAudioForKeyComposesEpisodesAndRows(t *testing.T) {
	f := newFakePMS(t)
	f.setLeaves(ep(1, 2, "2"), ep(1, 1, "1")) // unsorted; watched — unwatchedOnly must stay false
	f.addMeta("1", meta("1", 1001, audioStream("eng", 1, true, true, "")))
	f.addMeta("2", meta("2", 1002, audioStream("eng", 1, true, true, "")))

	rows := collect(streams.AuditAudioForKey("SHOW1", "eng", nil))
	if len(rows) != 2 {
		t.Fatalf("rows = %#v, want 2", rows)
	}
	// EpisodesForShowKey sorts ascending by (season, index).
	if rows[0]["episode"] != float64(1) || rows[1]["episode"] != float64(2) {
		t.Fatalf("rows not in season/episode order: %#v", rows)
	}
}

// --- resolveTrack (via SetAudioStream): precedence and id equivalence --------

func TestSetAudioStreamByLanguage(t *testing.T) {
	f := newFakePMS(t)
	f.addMeta("123", meta("123", 900,
		audioStream("deu", 1, true, false, ""),
		audioStream("eng", 2, false, false, ""),
	))

	out := streams.SetAudioStream("123", "eng", nil)
	if out["ok"] != true {
		t.Fatalf("ok = %#v, want true: %#v", out["ok"], out)
	}
	if out["partId"] != float64(900) || out["audioStreamID"] != float64(2) {
		t.Fatalf("out = %#v, want partId=900 audioStreamID=2", out)
	}
	q := lastQuery(f, isPartsPUT)
	if q.Get("audioStreamID") != "2" || q.Get("allParts") != "1" {
		t.Fatalf("PUT params = %v, want audioStreamID=2&allParts=1", q)
	}
	if !strings.Contains(pathOf(f, isPartsPUT), "/library/parts/900") {
		t.Fatalf("PUT path = %q, want /library/parts/900", pathOf(f, isPartsPUT))
	}
}

func TestSetAudioStreamByStreamID(t *testing.T) {
	f := newFakePMS(t)
	f.addMeta("123", meta("123", 900,
		audioStream("eng", 5, false, false, ""),
		audioStream("jpn", 6, false, false, ""),
	))

	out := streams.SetAudioStream("123", "", intPtr(6))
	if out["ok"] != true || out["audioStreamID"] != float64(6) {
		t.Fatalf("out = %#v, want ok=true audioStreamID=6", out)
	}
	q := lastQuery(f, isPartsPUT)
	if q.Get("audioStreamID") != "6" {
		t.Fatalf("PUT audioStreamID = %q, want 6", q.Get("audioStreamID"))
	}
}

func TestSetAudioStreamIDPrecedenceOverLanguage(t *testing.T) {
	// language="deu" would match id 1; stream_id=2 must win regardless.
	f := newFakePMS(t)
	f.addMeta("123", meta("123", 900,
		audioStream("deu", 1, false, false, ""),
		audioStream("eng", 2, false, false, ""),
	))

	out := streams.SetAudioStream("123", "deu", intPtr(2))
	if out["audioStreamID"] != float64(2) {
		t.Fatalf("audioStreamID = %#v, want 2 (streamID beats language)", out["audioStreamID"])
	}
}

func TestSetAudioStreamIDStringNumberEquivalence(t *testing.T) {
	cases := []struct {
		name string
		id   any
	}{
		{"numeric id", 6},
		{"string id", "6"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakePMS(t)
			f.addMeta("123", meta("123", 900, audioStream("eng", c.id, false, false, "")))
			out := streams.SetAudioStream("123", "", intPtr(6))
			if out["ok"] != true {
				t.Fatalf("out = %#v, want ok=true for id repr %v", out, c.id)
			}
		})
	}
}

func TestSetAudioStreamMissingLanguageErrorExact(t *testing.T) {
	f := newFakePMS(t)
	f.addMeta("123", meta("123", 900, audioStream("deu", 1, false, false, "")))

	out := streams.SetAudioStream("123", "eng", nil)
	if out["ok"] != false {
		t.Fatalf("ok = %#v, want false", out["ok"])
	}
	if out["error"] != "no eng audio track on 123" {
		t.Fatalf("error = %q, want exact %q", out["error"], "no eng audio track on 123")
	}
	if n := len(f.callsWhere(isPartsPUT)); n != 0 {
		t.Fatalf("PUT calls = %d, want 0", n)
	}
}

func TestSetAudioStreamMissingStreamIDErrorExact(t *testing.T) {
	f := newFakePMS(t)
	f.addMeta("123", meta("123", 900, audioStream("eng", 1, false, false, "")))

	out := streams.SetAudioStream("123", "", intPtr(99))
	if out["error"] != "no audio stream id 99 track on 123" {
		t.Fatalf("error = %q, want exact %q", out["error"], "no audio stream id 99 track on 123")
	}
}

func TestSetAudioStreamNoMetadataErrorExact(t *testing.T) {
	newFakePMS(t) // "999" never registered -> library.Metadata returns {}
	out := streams.SetAudioStream("999", "eng", nil)
	if out["ok"] != false || out["error"] != "no metadata for ratingKey 999" {
		t.Fatalf("out = %#v, want ok=false error=%q", out, "no metadata for ratingKey 999")
	}
}

// --- SetSubtitleStream --------------------------------------------------------

func TestSetSubtitleStreamOff(t *testing.T) {
	f := newFakePMS(t)
	f.addMeta("123", meta("123", 900, subtitleStream("eng", 3)))

	out := streams.SetSubtitleStream("123", "", nil, true)
	if out["ok"] != true || out["disabled"] != true {
		t.Fatalf("out = %#v, want ok=true disabled=true", out)
	}
	if out["subtitleStreamID"] != 0 {
		t.Fatalf("subtitleStreamID = %#v, want 0", out["subtitleStreamID"])
	}
	q := lastQuery(f, isPartsPUT)
	if q.Get("subtitleStreamID") != "0" || q.Get("allParts") != "1" {
		t.Fatalf("PUT params = %v, want subtitleStreamID=0&allParts=1", q)
	}
}

func TestSetSubtitleStreamByLanguage(t *testing.T) {
	newFakePMS(t).addMeta("123", meta("123", 900, subtitleStream("eng", 3), subtitleStream("spa", 4)))

	out := streams.SetSubtitleStream("123", "spa", nil, false)
	if out["ok"] != true || out["subtitleStreamID"] != float64(4) {
		t.Fatalf("out = %#v, want ok=true subtitleStreamID=4", out)
	}
}

func TestSetSubtitleStreamByStreamID(t *testing.T) {
	newFakePMS(t).addMeta("123", meta("123", 900, subtitleStream("eng", 3), subtitleStream("spa", 4)))

	out := streams.SetSubtitleStream("123", "", intPtr(3), false)
	if out["ok"] != true || out["subtitleStreamID"] != float64(3) {
		t.Fatalf("out = %#v, want ok=true subtitleStreamID=3", out)
	}
}

func TestSetSubtitleStreamMissingLanguageErrorExact(t *testing.T) {
	f := newFakePMS(t)
	f.addMeta("123", meta("123", 900, subtitleStream("eng", 3)))

	out := streams.SetSubtitleStream("123", "spa", nil, false)
	if out["error"] != "no spa subtitle track on 123" {
		t.Fatalf("error = %q, want exact %q", out["error"], "no spa subtitle track on 123")
	}
}

func TestSetSubtitleStreamMissingStreamIDErrorExact(t *testing.T) {
	f := newFakePMS(t)
	f.addMeta("123", meta("123", 900, subtitleStream("eng", 3)))

	out := streams.SetSubtitleStream("123", "", intPtr(42), false)
	if out["error"] != "no subtitle stream id 42 track on 123" {
		t.Fatalf("error = %q, want exact %q", out["error"], "no subtitle stream id 42 track on 123")
	}
}

func TestSetSubtitleStreamOffNoMediaPart(t *testing.T) {
	f := newFakePMS(t)
	f.addMeta("123", jsonx.J{"ratingKey": "123"}) // non-empty meta, no Media

	out := streams.SetSubtitleStream("123", "", nil, true)
	if out["ok"] != false || out["error"] != "no media part on 123" {
		t.Fatalf("out = %#v, want ok=false error=%q", out, "no media part on 123")
	}
}

func TestSetSubtitleStreamNoMetadataErrorExact(t *testing.T) {
	newFakePMS(t)
	out := streams.SetSubtitleStream("999", "eng", nil, false)
	if out["ok"] != false || out["error"] != "no metadata for ratingKey 999" {
		t.Fatalf("out = %#v, want no-metadata error", out)
	}
}

// --- PlanBulkAudio -------------------------------------------------------------

func TestPlanBulkAudioFromCodeSelectedThenDefault(t *testing.T) {
	f := newFakePMS(t)
	// ep10: selected != default -> fromCode is the selected code.
	f.addMeta("10", meta("10", 500,
		audioStream("deu", 1, true, false, ""),
		audioStream("eng", 2, false, true, ""),
	))
	// ep11: no selected, only default -> fromCode is the default code.
	f.addMeta("11", meta("11", 501, audioStream("jpn", 3, true, false, "")))
	// ep12: neither selected nor default -> fromCode nil.
	f.addMeta("12", meta("12", 502, audioStream("fra", 4, false, false, "")))

	episodes := []jsonx.J{ep(1, 1, "10"), ep(1, 2, "11"), ep(1, 3, "12")}
	plan := streams.PlanBulkAudio(episodes, "eng", false)
	if plan[0]["fromCode"] != "eng" {
		t.Fatalf("plan[0].fromCode = %#v, want eng (selected wins over default)", plan[0]["fromCode"])
	}
	if plan[1]["fromCode"] != "jpn" {
		t.Fatalf("plan[1].fromCode = %#v, want jpn (falls back to default)", plan[1]["fromCode"])
	}
	if plan[2]["fromCode"] != nil {
		t.Fatalf("plan[2].fromCode = %#v, want nil", plan[2]["fromCode"])
	}
	if n := len(f.callsWhere(isMetaGET)); n != 1 {
		t.Fatalf("metadata GET calls = %d, want exactly 1 (one batched read)", n)
	}
}

func TestPlanBulkAudioSkipReasonNoTrack(t *testing.T) {
	f := newFakePMS(t)
	f.addMeta("10", meta("10", 500, audioStream("deu", 1, true, false, "")))

	plan := streams.PlanBulkAudio([]jsonx.J{ep(1, 1, "10")}, "jpn", false)
	r := plan[0]
	if r["skip"] != true || r["reason"] != "no jpn audio track" {
		t.Fatalf("plan[0] = %#v, want skip=true reason=%q", r, "no jpn audio track")
	}
	if r["partId"] != nil || r["toStreamId"] != nil {
		t.Fatalf("plan[0] partId/toStreamId = %#v/%#v, want nil", r["partId"], r["toStreamId"])
	}
}

func TestPlanBulkAudioOnlyNonEngSkipsAlreadyPreferred(t *testing.T) {
	f := newFakePMS(t)
	f.addMeta("10", meta("10", 500, audioStream("eng", 2, false, true, ""))) // already eng-selected
	f.addMeta("11", meta("11", 501,
		audioStream("deu", 1, false, true, ""),
		audioStream("eng", 2, false, false, ""),
	))

	plan := streams.PlanBulkAudio([]jsonx.J{ep(4, 1, "10"), ep(4, 2, "11")}, "eng", true)
	if plan[0]["skip"] != true || plan[0]["reason"] != "already preferred" {
		t.Fatalf("plan[0] = %#v, want skip=true reason=already preferred", plan[0])
	}
	if plan[1]["skip"] != false {
		t.Fatalf("plan[1] = %#v, want skip=false (deu -> eng change)", plan[1])
	}
}

func TestPlanBulkAudioPairsStreamWithOwnPart(t *testing.T) {
	// Target stream id 7 lives on part 600, not the first part 500 — proves
	// reuse of resolveTrack's pairing, not first-part + flat-scan.
	f := newFakePMS(t)
	f.addMeta("10", metaWithParts("10",
		part(500, audioStream("deu", 3, false, true, "")),
		part(600, audioStream("eng", 7, false, false, "")),
	))

	plan := streams.PlanBulkAudio([]jsonx.J{ep(1, 1, "10")}, "eng", false)
	if plan[0]["partId"] != float64(600) || plan[0]["toStreamId"] != float64(7) {
		t.Fatalf("plan[0] = %#v, want partId=600 toStreamId=7", plan[0])
	}
}

// --- ExecuteBulkAudio ----------------------------------------------------------

func TestExecuteBulkAudioSkippedRowsNeverPut(t *testing.T) {
	f := newFakePMS(t)
	plan := []jsonx.J{
		{"ratingKey": "10", "partId": float64(500), "toStreamId": float64(2), "skip": true, "reason": "already preferred"},
	}
	results := streams.ExecuteBulkAudio(plan)
	if results[0]["status"] != "skipped" {
		t.Fatalf("status = %#v, want skipped", results[0]["status"])
	}
	if n := len(f.callsWhere(isPartsPUT)); n != 0 {
		t.Fatalf("PUT calls = %d, want 0 for a skipped row", n)
	}
}

func TestExecuteBulkAudioPerItemFailureTolerance(t *testing.T) {
	f := newFakePMS(t)
	f.enqueuePutStatus(500, 200) // first PUT fails, second succeeds
	plan := []jsonx.J{
		{"ratingKey": "10", "partId": float64(500), "toStreamId": float64(2), "skip": false},
		{"ratingKey": "11", "partId": float64(501), "toStreamId": float64(2), "skip": false},
	}

	results := streams.ExecuteBulkAudio(plan)
	if results[0]["status"] != "error" {
		t.Fatalf("results[0].status = %#v, want error", results[0]["status"])
	}
	errMsg, _ := results[0]["error"].(string)
	if !strings.HasPrefix(errMsg, "HTTP 500:") {
		t.Fatalf("results[0].error = %q, want HTTP 500: prefix", errMsg)
	}
	if results[1]["status"] != "ok" {
		t.Fatalf("results[1].status = %#v, want ok", results[1]["status"])
	}
	for _, c := range f.callsWhere(isPartsPUT) {
		if c.query.Get("allParts") != "1" {
			t.Fatalf("PUT %s allParts = %q, want 1", c.path, c.query.Get("allParts"))
		}
	}
}

func TestExecuteBulkAudioDoesNotMutateInput(t *testing.T) {
	newFakePMS(t)
	row := jsonx.J{"ratingKey": "10", "partId": float64(500), "toStreamId": float64(2), "skip": false}
	plan := []jsonx.J{row}

	streams.ExecuteBulkAudio(plan)
	if _, ok := row["status"]; ok {
		t.Fatalf("input row mutated: %#v", row)
	}
}

// --- small query/path helpers --------------------------------------------------

func lastQuery(f *fakePMS, pred func(capturedReq) bool) url.Values {
	calls := f.callsWhere(pred)
	if len(calls) == 0 {
		return url.Values{}
	}
	return calls[len(calls)-1].query
}

func pathOf(f *fakePMS, pred func(capturedReq) bool) string {
	calls := f.callsWhere(pred)
	if len(calls) == 0 {
		return ""
	}
	return calls[len(calls)-1].path
}
