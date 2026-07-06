package sessions_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/queuestate"
	"github.com/corinthian/plexctl/internal/sessions"
	"github.com/corinthian/plexctl/internal/testutil"
)

// --- fake PMS harness --------------------------------------------------------

type fakePMS struct {
	mu     sync.Mutex
	calls  []string
	routes map[string]func(r *http.Request) (int, any)
}

func newFakePMS(t *testing.T) *fakePMS {
	t.Helper()
	f := &fakePMS{routes: map[string]func(r *http.Request) (int, any){}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.URL.Path)
		handler, ok := f.routes[r.URL.Path]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		status, body := handler(r)
		if body == nil {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	testutil.Setup(t, srv.URL)
	return f
}

func (f *fakePMS) on(path string, fn func(r *http.Request) (int, any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[path] = fn
}

func (f *fakePMS) onJSON(path string, body any) {
	f.on(path, func(r *http.Request) (int, any) { return 200, body })
}

func (f *fakePMS) onStatus(path string, status int) {
	f.on(path, func(r *http.Request) (int, any) { return status, jsonx.J{} })
}

// withQuery wraps a JSON-body handler and records the last query string seen
// for that path into *dst.
func (f *fakePMS) withQuery(path string, dst *url.Values, body any) {
	f.on(path, func(r *http.Request) (int, any) {
		*dst = r.URL.Query()
		return 200, body
	})
}

// --- fixture builders --------------------------------------------------------

func anyList(items ...jsonx.J) []any {
	out := make([]any, len(items))
	for i, it := range items {
		out[i] = it
	}
	return out
}

func mc(container jsonx.J) jsonx.J {
	return jsonx.J{"MediaContainer": container}
}

func sessionEntry(machineID, state, title, typ string, extra jsonx.J) jsonx.J {
	e := jsonx.J{
		"title": title,
		"type":  typ,
		"Player": jsonx.J{
			"machineIdentifier": machineID,
			"state":             state,
		},
	}
	for k, v := range extra {
		e[k] = v
	}
	return e
}

func metadataPayload(ratingKey string, duration, year float64) jsonx.J {
	return mc(jsonx.J{"Metadata": anyList(jsonx.J{
		"ratingKey": ratingKey,
		"duration":  duration,
		"year":      year,
	})})
}

const client1 = "MID-1"

func testClient() jsonx.J {
	return jsonx.J{"machineIdentifier": client1, "name": "Apple TV", "baseurl": "http://x:32500"}
}

// --- NowPlaying ---------------------------------------------------------------

func TestNowPlayingFoundReturnsFullProjection(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/status/sessions", mc(jsonx.J{"Metadata": anyList(
		sessionEntry(client1, "playing", "Vertigo", "movie", jsonx.J{
			"grandparentTitle": nil,
			"parentIndex":      nil,
			"index":            nil,
			"year":             float64(1979),
			"viewOffset":       float64(33),
			"duration":         float64(5808333),
			"ratingKey":        "44727",
		}),
	)}))

	got := sessions.NowPlaying(testClient())

	want := jsonx.J{
		"ok": true, "state": "playing", "title": "Vertigo", "type": "movie",
		"show": nil, "season": nil, "episode": nil, "year": json.Number("1979"),
		"viewOffset": json.Number("33"), "duration": json.Number("5808333"), "ratingKey": "44727",
	}
	if len(got) != len(want) {
		t.Fatalf("got = %#v, want keys %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("key %q = %#v, want %#v (full: %#v)", k, got[k], v, got)
		}
	}
}

func TestNowPlayingTVEpisodeFields(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/status/sessions", mc(jsonx.J{"Metadata": anyList(
		sessionEntry(client1, "paused", "Pilot", "episode", jsonx.J{
			"grandparentTitle": "Some Show",
			"parentIndex":      float64(1),
			"index":            float64(3),
		}),
	)}))

	got := sessions.NowPlaying(testClient())
	if got["show"] != "Some Show" || got["season"] != json.Number("1") || got["episode"] != json.Number("3") {
		t.Fatalf("got = %#v, want show/season/episode populated", got)
	}
}

func TestNowPlayingIdleNoSessions(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/status/sessions", mc(jsonx.J{"Metadata": anyList()}))

	got := sessions.NowPlaying(testClient())
	want := jsonx.J{"ok": true, "state": "idle", "client": "Apple TV"}
	if len(got) != len(want) {
		t.Fatalf("got = %#v, want exactly ok/state/client (no title/ratingKey)", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("got[%q] = %#v, want %#v", k, got[k], v)
		}
	}
}

func TestNowPlayingIdleOnMachineIdentifierMismatch(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/status/sessions", mc(jsonx.J{"Metadata": anyList(
		sessionEntry("OTHER-MID", "playing", "Vertigo", "movie", jsonx.J{"ratingKey": "44727"}),
	)}))

	got := sessions.NowPlaying(testClient())
	if got["state"] != "idle" || got["client"] != "Apple TV" {
		t.Fatalf("got = %#v, want idle for mismatched client", got)
	}
	if _, ok := got["title"]; ok {
		t.Fatalf("got = %#v, idle shape must not carry title", got)
	}
}

func TestNowPlayingIdleFallsBackToMachineIdentifierWhenNameAbsent(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/status/sessions", mc(jsonx.J{"Metadata": anyList()}))

	got := sessions.NowPlaying(jsonx.J{"machineIdentifier": client1})
	if got["client"] != client1 {
		t.Fatalf("client = %#v, want machineIdentifier fallback %q", got["client"], client1)
	}
}

// --- CurrentRatingKey -----------------------------------------------------

func TestCurrentRatingKeyCoercesNumericRatingKeyWithoutDecimal(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/status/sessions", mc(jsonx.J{"Metadata": anyList(
		sessionEntry(client1, "playing", "Vertigo", "movie", jsonx.J{"ratingKey": float64(44727)}),
	)}))

	got := sessions.CurrentRatingKey(testClient())
	if got != "44727" {
		t.Fatalf("got = %q, want \"44727\" (no trailing .0)", got)
	}
}

func TestCurrentRatingKeyIdleReturnsEmptyString(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/status/sessions", mc(jsonx.J{"Metadata": anyList()}))

	if got := sessions.CurrentRatingKey(testClient()); got != "" {
		t.Fatalf("got = %q, want empty string when idle", got)
	}
}

// --- History ----------------------------------------------------------------

func TestHistorySendsBothPaginationParams(t *testing.T) {
	f := newFakePMS(t)
	var q url.Values
	f.withQuery("/status/sessions/history/all", &q, mc(jsonx.J{"Metadata": anyList()}))

	sessions.History(5)

	if q.Get("X-Plex-Container-Start") != "0" {
		t.Fatalf("X-Plex-Container-Start = %q, want 0", q.Get("X-Plex-Container-Start"))
	}
	if q.Get("X-Plex-Container-Size") != "5" {
		t.Fatalf("X-Plex-Container-Size = %q, want 5", q.Get("X-Plex-Container-Size"))
	}
	if q.Get("sort") != "viewedAt:desc" {
		t.Fatalf("sort = %q, want viewedAt:desc", q.Get("sort"))
	}
}

func TestHistoryRowProjectionAndFillDurationsIntegration(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/status/sessions/history/all", mc(jsonx.J{"Metadata": anyList(
		jsonx.J{ // missing duration/year, has ratingKey -> filled via fan-out
			"title": "Citizen Kane", "type": "movie",
			"viewedAt": float64(1778844492), "ratingKey": "44725",
		},
		jsonx.J{ // no ratingKey -> stays unfilled (best-effort)
			"title": "Deleted Item", "type": "movie",
			"viewedAt": float64(1778800000),
		},
	)}))
	f.onJSON("/library/metadata/44725", metadataPayload("44725", 6381245, 1983))

	got := sessions.History(10)
	if got["ok"] != true {
		t.Fatalf("ok = %#v, want true", got["ok"])
	}
	rows := got["history"].([]jsonx.J)
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0]["duration"] != json.Number("6381245") || rows[0]["year"] != json.Number("1983") {
		t.Fatalf("row 0 = %#v, want filled duration/year", rows[0])
	}
	if rows[1]["duration"] != nil || rows[1]["year"] != nil {
		t.Fatalf("row 1 (no ratingKey) = %#v, want duration/year still nil", rows[1])
	}
}

// --- ContinueWatching ---------------------------------------------------------

func TestContinueWatchingConcatenatesHubsAndProjects(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/continueWatching", mc(jsonx.J{"Hub": anyList(
		jsonx.J{"Metadata": anyList(jsonx.J{
			"title": "Ep1", "type": "episode", "grandparentTitle": "Show A",
			"parentIndex": float64(1), "index": float64(2),
			"viewOffset": float64(1000), "duration": float64(5000), "ratingKey": "1",
		})},
		jsonx.J{"Metadata": anyList(jsonx.J{
			"title": "Movie B", "type": "movie",
			"viewOffset": float64(200), "duration": float64(9000), "ratingKey": "2",
		})},
	)}))

	got := sessions.ContinueWatching()
	items := got["items"].([]jsonx.J)
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2 (concat across hubs)", len(items))
	}
	if items[0]["title"] != "Ep1" || items[0]["show"] != "Show A" || items[0]["season"] != json.Number("1") {
		t.Fatalf("item 0 = %#v", items[0])
	}
	if items[1]["title"] != "Movie B" || items[1]["ratingKey"] != "2" {
		t.Fatalf("item 1 = %#v", items[1])
	}
}

// --- Context: happy path -------------------------------------------------------

func TestContextHappyPathBundlesAllSections(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save(client1, "5594", "43652")

	f.onJSON("/status/sessions", mc(jsonx.J{"Metadata": anyList(
		sessionEntry(client1, "playing", "Vertigo", "movie", jsonx.J{
			"year": float64(1979), "viewOffset": float64(33),
			"duration": float64(5808333), "ratingKey": "44727",
		}),
	)}))
	f.onJSON("/playQueues/5594", mc(jsonx.J{
		"playQueueSelectedItemID": float64(43652),
		"Metadata": anyList(
			jsonx.J{"playQueueItemID": float64(43652), "title": "Vertigo", "type": "movie",
				"ratingKey": "44727", "duration": float64(5808333), "year": float64(1979)},
			jsonx.J{"playQueueItemID": float64(43653), "title": "Citizen Kane", "type": "movie",
				"ratingKey": "44725", "duration": float64(6381245), "year": float64(1983)},
		),
	}))
	f.onJSON("/status/sessions/history/all", mc(jsonx.J{"Metadata": anyList(
		jsonx.J{"title": "Citizen Kane", "type": "movie", "viewedAt": float64(1778844492),
			"ratingKey": "44725", "duration": float64(6381245), "year": float64(1983)},
		jsonx.J{"title": "Mid-Era Episode", "type": "episode", "grandparentTitle": "Some Show",
			"viewedAt": float64(1778800000), "ratingKey": "44900"},
	)}))
	f.onJSON("/library/metadata/44725", metadataPayload("44725", 6381245, 1983))
	f.onJSON("/library/metadata/44900", metadataPayload("44900", 1320000, 2018))

	result := sessions.Context(testClient(), 5, true)

	if result["ok"] != true {
		t.Fatalf("ok = %#v, want true", result["ok"])
	}
	if result["client"] != "Apple TV" {
		t.Fatalf("client = %#v, want Apple TV", result["client"])
	}
	if _, ok := result["fetchedAt"].(int); !ok {
		t.Fatalf("fetchedAt = %#v, want int", result["fetchedAt"])
	}
	if _, ok := result["elapsedMs"].(int); !ok {
		t.Fatalf("elapsedMs = %#v, want int", result["elapsedMs"])
	}

	np := result["nowPlaying"].(jsonx.J)
	if np["ok"] != true || np["title"] != "Vertigo" || np["state"] != "playing" || np["ratingKey"] != "44727" {
		t.Fatalf("nowPlaying = %#v", np)
	}

	q := result["queue"].(jsonx.J)
	if q["ok"] != true || q["playQueueID"] != "5594" {
		t.Fatalf("queue = %#v", q)
	}
	items := q["items"].([]jsonx.J)
	if len(items) != 2 || items[0]["ratingKey"] != "44727" || items[0]["duration"] != json.Number("5808333") {
		t.Fatalf("queue items = %#v", items)
	}
	if items[0]["selected"] != true || items[1]["selected"] != false {
		t.Fatalf("selected flags = %#v / %#v, want true/false", items[0]["selected"], items[1]["selected"])
	}

	h := result["history"].(jsonx.J)
	if h["ok"] != true {
		t.Fatalf("history = %#v", h)
	}
	hItems := h["items"].([]jsonx.J)
	if len(hItems) != 2 {
		t.Fatalf("len(history items) = %d, want 2", len(hItems))
	}
	if hItems[0]["duration"] != json.Number("6381245") || hItems[0]["year"] != json.Number("1983") {
		t.Fatalf("history row 0 = %#v, want preserved duration/year", hItems[0])
	}
	if hItems[1]["duration"] != json.Number("1320000") || hItems[1]["year"] != json.Number("2018") {
		t.Fatalf("history row 1 = %#v, want filled via metadata fan-out", hItems[1])
	}
	if hItems[1]["show"] != "Some Show" {
		t.Fatalf("history row 1 show = %#v, want Some Show", hItems[1]["show"])
	}
}

func TestContextNoHistorySkipsHistoryKey(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save(client1, "5594", "43652")
	f.onJSON("/status/sessions", mc(jsonx.J{"Metadata": anyList()}))
	f.onJSON("/playQueues/5594", mc(jsonx.J{"Metadata": anyList()}))

	result := sessions.Context(testClient(), 5, false)

	if _, ok := result["history"]; ok {
		t.Fatalf("result = %#v, must not carry history key when includeHistory=false", result)
	}
	if result["nowPlaying"].(jsonx.J)["ok"] != true || result["queue"].(jsonx.J)["ok"] != true {
		t.Fatalf("sections should still succeed: %#v", result)
	}
}

func TestContextEmptyQueueWhenNoStateEntry(t *testing.T) {
	f := newFakePMS(t)
	// No queuestate.Save call — resolver finds no qid.
	f.onJSON("/status/sessions", mc(jsonx.J{"Metadata": anyList()}))
	f.onJSON("/status/sessions/history/all", mc(jsonx.J{"Metadata": anyList()}))

	result := sessions.Context(testClient(), 5, true)

	q := result["queue"].(jsonx.J)
	if q["ok"] != true || q["state"] != "empty" {
		t.Fatalf("queue = %#v, want ok:true state:empty", q)
	}
	if q["playQueueID"] != nil {
		t.Fatalf("playQueueID = %#v, want nil", q["playQueueID"])
	}
	if items, ok := q["items"].([]jsonx.J); !ok || len(items) != 0 {
		t.Fatalf("items = %#v, want empty slice", q["items"])
	}
}

func TestContextStaleQidWithEmptyPMSQueueReturnsEmptyStateWithStringQid(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save(client1, "5594", "43652")
	f.onJSON("/status/sessions", mc(jsonx.J{"Metadata": anyList()}))
	f.onJSON("/playQueues/5594", mc(jsonx.J{"Metadata": anyList()})) // queue emptied server-side
	f.onJSON("/status/sessions/history/all", mc(jsonx.J{"Metadata": anyList()}))

	result := sessions.Context(testClient(), 5, true)

	q := result["queue"].(jsonx.J)
	if q["ok"] != true || q["state"] != "empty" {
		t.Fatalf("queue = %#v, want ok:true state:empty", q)
	}
	if q["playQueueID"] != "5594" {
		t.Fatalf("playQueueID = %#v, want string \"5594\"", q["playQueueID"])
	}
	if q["selectedItemID"] != nil {
		t.Fatalf("selectedItemID = %#v, want nil", q["selectedItemID"])
	}
}

func TestContextIdleNowPlayingWhenSessionBelongsToOtherClient(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/status/sessions", mc(jsonx.J{"Metadata": anyList(
		sessionEntry("OTHER-MID", "playing", "Vertigo", "movie", jsonx.J{"ratingKey": "44727"}),
	)}))
	f.onJSON("/status/sessions/history/all", mc(jsonx.J{"Metadata": anyList()}))

	result := sessions.Context(testClient(), 5, true)

	np := result["nowPlaying"].(jsonx.J)
	if np["ok"] != true || np["state"] != "idle" {
		t.Fatalf("nowPlaying = %#v, want ok:true state:idle", np)
	}
	if _, ok := np["client"]; ok {
		t.Fatalf("nowPlaying = %#v, context's idle shape must not carry a client key", np)
	}
	if result["ok"] != true {
		t.Fatalf("top-level ok = %#v, want true (idle is not a failure)", result["ok"])
	}
}

func TestContextQueueFailureDegradesSectionOnlyTopLevelTracksNowPlaying(t *testing.T) {
	// A non-404 server error on the queue read degrades that section to
	// ok:false but leaves the top-level ok tracking nowPlaying. (404 is a
	// distinct path now — see TestContextStaleQueue404KeepsStateAndDegradesToEmpty.)
	f := newFakePMS(t)
	queuestate.Save(client1, "5594", "43652")
	f.onJSON("/status/sessions", mc(jsonx.J{"Metadata": anyList(
		sessionEntry(client1, "playing", "Vertigo", "movie", jsonx.J{"ratingKey": "44727"}),
	)}))
	f.onStatus("/playQueues/5594", 500)
	f.onJSON("/status/sessions/history/all", mc(jsonx.J{"Metadata": anyList()}))

	result := sessions.Context(testClient(), 5, true)

	if result["ok"] != true {
		t.Fatalf("top-level ok = %#v, want true (nowPlaying succeeded)", result["ok"])
	}
	if result["nowPlaying"].(jsonx.J)["ok"] != true {
		t.Fatalf("nowPlaying = %#v, want ok:true", result["nowPlaying"])
	}
	q := result["queue"].(jsonx.J)
	if q["ok"] != false {
		t.Fatalf("queue = %#v, want ok:false", q)
	}
	if errStr, _ := q["error"].(string); errStr == "" {
		t.Fatalf("queue error missing: %#v", q)
	}
}

// C4 (finding 7): a saved queue id that 404s makes Context degrade the queue
// section to empty (ok:true) rather than failing it — nowPlaying + history stay
// intact. It must NOT delete the saved entry: a transient 404 on this latency-
// sensitive startup fetch must not destroy an addressable queue.
func TestContextStaleQueue404KeepsStateAndDegradesToEmpty(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save(client1, "5594", "43652")
	f.onJSON("/status/sessions", mc(jsonx.J{"Metadata": anyList(
		sessionEntry(client1, "playing", "Vertigo", "movie", jsonx.J{"ratingKey": "44727"}),
	)}))
	f.onStatus("/playQueues/5594", 404)
	f.onJSON("/status/sessions/history/all", mc(jsonx.J{"Metadata": anyList(
		jsonx.J{"title": "Citizen Kane", "type": "movie", "viewedAt": float64(1778844492), "ratingKey": "44725", "duration": float64(6381245), "year": float64(1983)},
	)}))

	result := sessions.Context(testClient(), 5, true)

	if result["ok"] != true {
		t.Fatalf("top-level ok = %#v, want true", result["ok"])
	}
	q := result["queue"].(jsonx.J)
	if q["ok"] != true || q["state"] != "empty" {
		t.Fatalf("queue = %#v, want ok:true state:empty", q)
	}
	if q["playQueueID"] != nil {
		t.Fatalf("playQueueID = %#v, want nil (pruned)", q["playQueueID"])
	}
	if queuestate.Load(client1) == nil {
		t.Fatalf("state must be KEPT on 404 (finding 7) — a transient 404 must not delete an addressable queue")
	}
	// nowPlaying + history survive the stale queue.
	if result["nowPlaying"].(jsonx.J)["ok"] != true {
		t.Fatalf("nowPlaying = %#v, want ok:true", result["nowPlaying"])
	}
	h := result["history"].(jsonx.J)
	if h["ok"] != true {
		t.Fatalf("history = %#v, want ok:true", h)
	}
	if items := h["items"].([]jsonx.J); len(items) != 1 {
		t.Fatalf("history items = %#v, want 1", items)
	}
}

func TestContextNowPlayingFailureMarksTopLevelNotOk(t *testing.T) {
	f := newFakePMS(t)
	f.onStatus("/status/sessions", 500)
	f.onJSON("/status/sessions/history/all", mc(jsonx.J{"Metadata": anyList()}))

	result := sessions.Context(testClient(), 5, true)

	if result["ok"] != false {
		t.Fatalf("top-level ok = %#v, want false", result["ok"])
	}
	np := result["nowPlaying"].(jsonx.J)
	if np["ok"] != false {
		t.Fatalf("nowPlaying = %#v, want ok:false", np)
	}
	if errStr, _ := np["error"].(string); errStr == "" {
		t.Fatalf("nowPlaying error missing: %#v", np)
	}
}
