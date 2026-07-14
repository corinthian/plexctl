package library_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/library"
	"github.com/corinthian/plexctl/internal/testutil"
)

// --- fake PMS harness --------------------------------------------------------

type capturedReq struct {
	path  string
	query url.Values
}

type fakePMS struct {
	mu     sync.Mutex
	calls  []capturedReq
	routes map[string]func(r *http.Request) (int, any)
}

func newFakePMS(t *testing.T) *fakePMS {
	t.Helper()
	f := &fakePMS{routes: map[string]func(r *http.Request) (int, any){}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, capturedReq{path: r.URL.Path, query: r.URL.Query()})
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
	f.on(path, func(r *http.Request) (int, any) { return status, nil })
}

func (f *fakePMS) callCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.path == path {
			n++
		}
	}
	return n
}

func (f *fakePMS) lastQuery(path string) url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	var last url.Values
	for _, c := range f.calls {
		if c.path == path {
			last = c.query
		}
	}
	return last
}

// --- fixture builders --------------------------------------------------------

func anyList(items ...jsonx.J) []any {
	out := make([]any, len(items))
	for i, it := range items {
		out[i] = it
	}
	return out
}

func hub(hubType string, items ...jsonx.J) jsonx.J {
	h := jsonx.J{"Metadata": anyList(items...)}
	if hubType != "" {
		h["type"] = hubType
	}
	return h
}

func hubResp(hubs ...jsonx.J) jsonx.J {
	return jsonx.J{"MediaContainer": jsonx.J{"Hub": anyList(hubs...)}}
}

func leavesResp(eps ...jsonx.J) jsonx.J {
	return jsonx.J{"MediaContainer": jsonx.J{"Metadata": anyList(eps...)}}
}

func metaResp(items ...jsonx.J) jsonx.J {
	return jsonx.J{"MediaContainer": jsonx.J{"Metadata": anyList(items...)}}
}

func dirsResp(dirs ...jsonx.J) jsonx.J {
	return jsonx.J{"MediaContainer": jsonx.J{"Directory": anyList(dirs...)}}
}

func ep(season, index int, viewCount int, aired, title, key string) jsonx.J {
	return jsonx.J{
		"ratingKey":             key,
		"parentIndex":           float64(season),
		"index":                 float64(index),
		"title":                 title,
		"viewCount":             float64(viewCount),
		"originallyAvailableAt": aired,
		"grandparentTitle":      "Some Show",
	}
}

func titles(items []jsonx.J) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i], _ = it["title"].(string)
	}
	return out
}

// --- search: ranked hub first, voice hub last ---------------------------------
//
// Fixture scores mirror the live server, which normalises `score` to 0..1 and
// never returns 1.0. Measured against a real library: an exact title match tops
// out at ~0.93, a prefix match ("Alien" → Aliens) lands ~0.53, and a weak-but-real
// partial ("Angry Men" → 12 Angry Men) shares the ~0.33 band with pure noise
// ("Godfather" → His Dark Materials). The voice hub omits `score` entirely.
//
// The v1.0.0 fixtures asserted scores of 2.5, 3.0 and 5 — values PMS cannot
// produce. That is why a 1.0 default floor passed the suite while rejecting
// every result the real server can return.

func TestSearchPrefersRankedHubOverVoice(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("movie",
		jsonx.J{"title": "Alien", "ratingKey": "107", "score": "0.93084"},
	)))
	f.onJSON("/hubs/search/voice", hubResp(hub("movie",
		jsonx.J{"title": "Akira", "ratingKey": "31"},
	))) // must not be consulted — this is where "Alien" used to return Akira

	results := library.Search("alien", "", library.TightMinScore)

	if got := titles(results); len(got) != 1 || got[0] != "Alien" {
		t.Fatalf("results = %v, want [Alien]", got)
	}
	if f.callCount("/hubs/search/voice") != 0 {
		t.Fatalf("voice hub called %d times, want 0 — it is a last resort, not a first choice",
			f.callCount("/hubs/search/voice"))
	}
}

func TestSearchFallsBackToVoiceOnlyWhenNothingScores(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp()) // ranked hub knows nothing
	f.onJSON("/hubs/search/voice", hubResp(hub("show",
		jsonx.J{"title": "Black Books", "ratingKey": "1440"},
	)))

	results := library.Search("blakebux", "", library.TightMinScore)

	if got := titles(results); len(got) != 1 || got[0] != "Black Books" {
		t.Fatalf("results = %v, want [Black Books] — mangled dictation is what voice is for", got)
	}
}

// The voice hub omits `score`, so every item coerces to 0.0. Applying a floor to
// it discards the entire response — the v1.0.0 bug in miniature.
func TestSearchNeverScoreFiltersTheVoiceHub(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp())
	f.onJSON("/hubs/search/voice", hubResp(hub("movie",
		jsonx.J{"title": "Unscored", "ratingKey": "1"},
	)))

	results := library.Search("q", "", library.TightMinScore)

	if got := titles(results); len(got) != 1 || got[0] != "Unscored" {
		t.Fatalf("results = %v, want [Unscored] — voice results must survive a score floor", got)
	}
}

// The regression guard: a real exact match must clear the default floor.
func TestSearchExactMatchClearsDefaultFloor(t *testing.T) {
	if library.TightMinScore >= 0.93 {
		t.Fatalf("TightMinScore = %v — unreachable; no PMS result can score that high",
			library.TightMinScore)
	}
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("show",
		jsonx.J{"title": "Black Books", "ratingKey": "1440", "score": "0.93071"},
	)))

	results, loose := library.SearchTiered("Black Books", "show")

	if got := titles(results); len(got) != 1 || got[0] != "Black Books" {
		t.Fatalf("results = %v, want [Black Books]", got)
	}
	if loose {
		t.Fatal("loose = true for a 0.93 exact match, want false")
	}
}

func TestSearchTieredDropsNoiseWhenAConfidentHitExists(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("movie",
		jsonx.J{"title": "Alien", "ratingKey": "107", "score": "0.93084"},
		jsonx.J{"title": "Aliens", "ratingKey": "108", "score": "0.53084"},
		jsonx.J{"title": "Noise", "ratingKey": "999", "score": "0.33001"},
	)))

	results, loose := library.SearchTiered("Alien", "")

	if got := titles(results); len(got) != 2 || got[0] != "Alien" || got[1] != "Aliens" {
		t.Fatalf("results = %v, want [Alien Aliens] — 0.33-band noise must not ride along", got)
	}
	if loose {
		t.Fatal("loose = true despite a confident hit, want false")
	}
}

func TestSearchTieredWidensAndFlagsLooseWhenOnlyWeakHits(t *testing.T) {
	f := newFakePMS(t)
	// "Angry Men" → 12 Angry Men is genuinely what the user meant, but PMS scores
	// it in the same band as noise. Return it, and admit the uncertainty.
	f.onJSON("/hubs/search", hubResp(hub("movie",
		jsonx.J{"title": "12 Angry Men", "ratingKey": "37", "score": "0.33090"},
	)))
	f.onJSON("/hubs/search/voice", hubResp()) // must not be needed

	results, loose := library.SearchTiered("Angry Men", "")

	if got := titles(results); len(got) != 1 || got[0] != "12 Angry Men" {
		t.Fatalf("results = %v, want [12 Angry Men]", got)
	}
	if !loose {
		t.Fatal("loose = false for a 0.33-band hit, want true — the caller must be able to hedge")
	}
	if f.callCount("/hubs/search") != 1 {
		t.Fatalf("ranked hub called %d times, want 1 — tiering partitions locally, it does not re-fetch",
			f.callCount("/hubs/search"))
	}
}

// ResolveShow reads index 0, so cross-hub ordering is load-bearing.
func TestSearchSortsBestHitFirstAcrossHubs(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(
		hub("movie", jsonx.J{"title": "Weak Movie", "score": "0.53000"}),
		hub("show", jsonx.J{"title": "Strong Show", "score": "0.93000"}),
	))

	results, _ := library.SearchTiered("q", "")

	if got := titles(results); len(got) != 2 || got[0] != "Strong Show" {
		t.Fatalf("results = %v, want Strong Show first — hub order is not relevance order", got)
	}
}

// --- search: explicit floor ---------------------------------------------------

func TestSearchMinScoreZeroDisablesFilter(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("movie",
		jsonx.J{"title": "Real", "ratingKey": "1", "score": "0.93"},
		jsonx.J{"title": "Junk", "ratingKey": "2", "score": "0.33"},
		jsonx.J{"title": "NoScore", "ratingKey": "3"},
	)))

	results := library.Search("asdfqwerzxcv", "", 0)
	want := []string{"Real", "Junk", "NoScore"} // score-desc; unscored sinks last
	got := titles(results)
	if len(got) != len(want) {
		t.Fatalf("results = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("results = %v, want %v", got, want)
		}
	}
}

func TestSearchDropsUnparseableAndMissingScoresAtAFloor(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("movie",
		jsonx.J{"title": "Exact", "score": "0.93080"},
		jsonx.J{"title": "Prefix", "score": 0.53},
		jsonx.J{"title": "Weak", "score": "0.33000"},
		jsonx.J{"title": "Missing"},
		jsonx.J{"title": "Garbage", "score": "not-a-number"},
		jsonx.J{"title": "NullScore", "score": nil},
	)))
	f.onJSON("/hubs/search/voice", hubResp()) // must not fire; results are non-empty

	results := library.Search("asdf", "", library.TightMinScore)
	if got := titles(results); len(got) != 2 || got[0] != "Exact" || got[1] != "Prefix" {
		t.Fatalf("results = %v, want [Exact Prefix]", got)
	}
}

// --- search: hub type filtering -----------------------------------------------

func TestSearchFiltersByHubType(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(
		hub("movie", jsonx.J{"title": "MovieHit", "score": "0.93"}),
		hub("show", jsonx.J{"title": "ShowHit", "score": "0.93"}),
	))

	if got := titles(library.Search("q", "show", 0)); len(got) != 1 || got[0] != "ShowHit" {
		t.Fatalf("show-filtered results = %v, want [ShowHit]", got)
	}
	if got := titles(library.Search("q", "movie", 0)); len(got) != 1 || got[0] != "MovieHit" {
		t.Fatalf("movie-filtered results = %v, want [MovieHit]", got)
	}
	if got := titles(library.Search("q", "", 0)); len(got) != 2 {
		t.Fatalf("unfiltered results = %v, want both hubs' items", got)
	}
}

func TestSearchSendsTypeCodeForKnownMediaType(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("show", jsonx.J{"title": "Show", "score": "0.93"})))

	library.Search("q", "show", 0)

	q := f.lastQuery("/hubs/search")
	if q.Get("type") != "2" {
		t.Fatalf("type param = %q, want 2 (show)", q.Get("type"))
	}
	if q.Get("query") != "q" || q.Get("limit") != "10" {
		t.Fatalf("params = %v, want query=q&limit=10", q)
	}
}

func TestSearchOmitsTypeCodeWhenUnfiltered(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("movie", jsonx.J{"title": "M", "score": "0.93"})))

	library.Search("rocky", "", 0)

	if q := f.lastQuery("/hubs/search"); q.Has("type") {
		t.Fatalf("params carried unwanted type= for an unfiltered search: %v", q)
	}
}

func TestSearchEmptyResultMarshalsAsJSONArrayNotNull(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp())
	f.onJSON("/hubs/search/voice", hubResp())

	results := library.Search("asdfqwerzxcv", "", library.TightMinScore)
	if got := jsonx.Marshal(results); got != "[]" {
		t.Fatalf("Marshal(empty results) = %q, want \"[]\" (never null on stdout)", got)
	}
}

// --- resolve_show -------------------------------------------------------------

// A niche show scoring in the weak band must still resolve — that is what the
// loose tier is for. It just has to be ranked before index 0 is read.
func TestResolveShowWidensToTheWeakBand(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("show",
		jsonx.J{"title": "NicheShow", "ratingKey": "99", "score": "0.33"},
	)))

	hit := library.ResolveShow("niche")
	if hit == nil || hit["ratingKey"] != "99" {
		t.Fatalf("hit = %#v, want ratingKey 99 — a weak score is still a hit", hit)
	}
}

func TestResolveShowPrefersStrongestHit(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("show",
		jsonx.J{"title": "Weak", "ratingKey": "1", "score": "0.53"},
		jsonx.J{"title": "Exact", "ratingKey": "2", "score": "0.93"},
	)))

	hit := library.ResolveShow("exact")
	if hit == nil || hit["ratingKey"] != "2" {
		t.Fatalf("hit = %#v, want ratingKey 2 (the 0.93 hit), not whatever the hub listed first", hit)
	}
}

func TestResolveShowNoHitsReturnsNil(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp())
	f.onJSON("/hubs/search/voice", hubResp())

	if hit := library.ResolveShow("nonexistent"); hit != nil {
		t.Fatalf("hit = %#v, want nil", hit)
	}
}

// --- episodes_for_show_key: ordering and filters ------------------------------

func TestEpisodesForShowKeySortsAscendingBySeasonEpisode(t *testing.T) {
	f := newFakePMS(t)
	unsorted := []jsonx.J{
		ep(2, 1, 0, "", "", "201"), ep(1, 2, 0, "", "", "102"), ep(1, 1, 0, "", "", "101"),
		ep(2, 10, 0, "", "", "210"), ep(2, 2, 0, "", "", "202"),
	}
	f.onJSON("/library/metadata/SHOW1/allLeaves", leavesResp(unsorted...))

	eps := library.EpisodesForShowKey("SHOW1", false, nil)
	var got [][2]int
	for _, e := range eps {
		got = append(got, [2]int{int(jsonx.Num(e["parentIndex"])), int(jsonx.Num(e["index"]))})
	}
	want := [][2]int{{1, 1}, {1, 2}, {2, 1}, {2, 2}, {2, 10}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestEpisodesForShowKeySeasonFilter(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/metadata/SHOW1/allLeaves", leavesResp(
		ep(1, 1, 0, "", "", "1"), ep(1, 2, 0, "", "", "2"), ep(2, 1, 0, "", "", "3"),
	))
	season := 2
	eps := library.EpisodesForShowKey("SHOW1", false, &season)
	if len(eps) != 1 || jsonx.Num(eps[0]["parentIndex"]) != 2 {
		t.Fatalf("eps = %#v, want single season-2 episode", eps)
	}
}

func TestEpisodesForShowKeyUnwatchedFilter(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/metadata/SHOW1/allLeaves", leavesResp(
		ep(1, 1, 1, "", "", "1"), ep(1, 2, 0, "", "", "2"), ep(1, 3, 2, "", "", "3"),
	))
	eps := library.EpisodesForShowKey("SHOW1", true, nil)
	if len(eps) != 1 || jsonx.Num(eps[0]["index"]) != 2 {
		t.Fatalf("eps = %#v, want single unwatched episode (index 2)", eps)
	}
}

func TestEpisodesForShowKeySeasonAndUnwatchedCombine(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/metadata/SHOW1/allLeaves", leavesResp(
		ep(1, 1, 0, "", "", "1"), ep(2, 1, 1, "", "", "2"), ep(2, 2, 0, "", "", "3"),
	))
	season := 2
	eps := library.EpisodesForShowKey("SHOW1", true, &season)
	if len(eps) != 1 || jsonx.Num(eps[0]["index"]) != 2 || jsonx.Num(eps[0]["parentIndex"]) != 2 {
		t.Fatalf("eps = %#v, want single (2,2) episode", eps)
	}
}

func TestEpisodesForShowKeyNumericKeyRendersWithoutDecimal(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/metadata/500/allLeaves", leavesResp())
	// showKey arrives as float64 straight from PMS JSON (e.g. a ratingKey field).
	eps := library.EpisodesForShowKey(float64(500), false, nil)
	if len(eps) != 0 {
		t.Fatalf("eps = %#v, want empty", eps)
	}
	if f.callCount("/library/metadata/500/allLeaves") != 1 {
		t.Fatal("expected the integral float64 key to render as \"500\", not \"500.0\"")
	}
}

// --- show_episodes: resolves then enumerates ----------------------------------

func TestShowEpisodesResolvesThenEnumerates(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("show", jsonx.J{"ratingKey": "SHOW1", "title": "Some Show", "score": "0.93080"})))
	f.onJSON("/library/metadata/SHOW1/allLeaves", leavesResp(ep(1, 2, 0, "", "", "2"), ep(1, 1, 0, "", "", "1")))

	eps := library.ShowEpisodes("some show", false, nil)
	if len(eps) != 2 || jsonx.Num(eps[0]["index"]) != 1 {
		t.Fatalf("eps = %#v, want sorted [1,2]", eps)
	}
}

func TestShowEpisodesNoMatchReturnsEmptyWithoutFetch(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search/voice", hubResp())
	f.onJSON("/hubs/search", hubResp())

	eps := library.ShowEpisodes("nonexistent", false, nil)
	if len(eps) != 0 {
		t.Fatalf("eps = %#v, want empty", eps)
	}
}

func TestShowEpisodesNoLeavesReturnsEmpty(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("show", jsonx.J{"ratingKey": "SHOW1", "title": "Some Show", "score": "0.93080"})))
	f.onJSON("/library/metadata/SHOW1/allLeaves", leavesResp())

	eps := library.ShowEpisodes("some show", false, nil)
	if len(eps) != 0 {
		t.Fatalf("eps = %#v, want empty", eps)
	}
}

// --- latest_unwatched_episode --------------------------------------------------

func TestLatestUnwatchedReturnsFirstUnwatched(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("show", jsonx.J{"ratingKey": "SHOW1", "title": "Some Show", "score": "0.93080"})))
	f.onJSON("/library/metadata/SHOW1/allLeaves", leavesResp(
		ep(1, 1, 1, "2019-01-01", "Watched", "1"),
		ep(1, 2, 0, "2019-02-01", "NextUp", "2"),
		ep(1, 3, 0, "2019-03-01", "Later", "3"),
	))

	got := library.LatestUnwatchedEpisode("some show", false)
	if got == nil || got["title"] != "NextUp" {
		t.Fatalf("got = %#v, want NextUp", got)
	}
}

func TestLatestUnwatchedStrictReturnsNilWhenAllWatched(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("show", jsonx.J{"ratingKey": "SHOW1", "title": "Some Show", "score": "0.93080"})))
	f.onJSON("/library/metadata/SHOW1/allLeaves", leavesResp(
		ep(1, 1, 1, "", "", "1"), ep(1, 2, 3, "", "", "2"),
	))

	if got := library.LatestUnwatchedEpisode("some show", true); got != nil {
		t.Fatalf("got = %#v, want nil", got)
	}
}

func TestLatestUnwatchedFallsBackToLatestAiredNotPilot(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("show", jsonx.J{"ratingKey": "SHOW1", "title": "Some Show", "score": "0.93080"})))
	f.onJSON("/library/metadata/SHOW1/allLeaves", leavesResp(
		ep(1, 1, 1, "2019-01-01", "Pilot", "1"),
		ep(1, 2, 1, "2019-02-01", "", "2"),
		ep(2, 5, 1, "2021-06-01", "Newest", "3"),
		ep(2, 1, 1, "2020-01-01", "", "4"),
	))

	got := library.LatestUnwatchedEpisode("some show", false)
	if got == nil || got["title"] != "Newest" {
		t.Fatalf("got = %#v, want Newest (latest aired, not the pilot)", got)
	}
}

func TestLatestUnwatchedTieBreakKeepsFirstMaximal(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search", hubResp(hub("show", jsonx.J{"ratingKey": "SHOW1", "title": "Some Show", "score": "0.93080"})))
	// Both watched, tied on originallyAvailableAt; sorted order puts (1,1) before (1,2).
	f.onJSON("/library/metadata/SHOW1/allLeaves", leavesResp(
		ep(1, 1, 1, "2020-01-01", "First", "1"),
		ep(1, 2, 1, "2020-01-01", "Second", "2"),
	))

	got := library.LatestUnwatchedEpisode("some show", false)
	if got == nil || got["title"] != "First" {
		t.Fatalf("got = %#v, want First (Python max() keeps the first maximal element)", got)
	}
}

func TestLatestUnwatchedNoShowMatchReturnsNil(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/hubs/search/voice", hubResp())
	f.onJSON("/hubs/search", hubResp())

	if got := library.LatestUnwatchedEpisode("nonexistent", false); got != nil {
		t.Fatalf("got = %#v, want nil", got)
	}
	if f.callCount("/library/metadata/SHOW1/allLeaves") != 0 {
		t.Fatal("no show resolved, must not fetch allLeaves")
	}
}

// --- sections ------------------------------------------------------------------

func TestSectionsProjectsOnlyKeyTitleType(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/sections", dirsResp(
		jsonx.J{"key": "1", "title": "All Movies", "type": "movie", "scanner": "Plex Movie", "agent": "tv.plex"},
		jsonx.J{"key": "3", "title": "All Episodes", "type": "show"},
	))

	rows := library.Sections()
	if len(rows) != 2 {
		t.Fatalf("rows = %#v, want 2", rows)
	}
	if len(rows[0]) != 3 || rows[0]["key"] != "1" || rows[0]["title"] != "All Movies" || rows[0]["type"] != "movie" {
		t.Fatalf("row 0 = %#v, want exactly key/title/type", rows[0])
	}
}

// --- list_section: unwatched client-side filter (shows vs movies) --------------

func showItem(key, title string, leaves, viewed int, viewCount *int) jsonx.J {
	item := jsonx.J{"ratingKey": key, "title": title, "type": "show",
		"leafCount": float64(leaves), "viewedLeafCount": float64(viewed)}
	if viewCount != nil {
		item["viewCount"] = float64(*viewCount)
	}
	return item
}

func movieItem(key, title string, viewCount int) jsonx.J {
	return jsonx.J{"ratingKey": key, "title": title, "type": "movie", "viewCount": float64(viewCount)}
}

func intPtr(v int) *int { return &v }

func TestListSectionShowsFilteredOnLeafCountersNotPlayHistory(t *testing.T) {
	f := newFakePMS(t)
	items := []jsonx.J{
		showItem("1961", "Speed Racer", 26, 26, nil),      // fully watched, never "played"
		showItem("2001", "The Office", 8, 0, intPtr(38)),  // unwatched but has play history
		showItem("2002", "The Simpsons", 6, 2, intPtr(9)), // partial
		showItem("2003", "Columbo", 10, 0, nil),           // fully unwatched, no history
	}
	f.onJSON("/library/sections/3/all", metaResp(items...))

	rows := library.ListSection("3", "show", true, "")
	got := titles(rows)
	want := map[string]bool{"The Office": true, "The Simpsons": true, "Columbo": true}
	for _, title := range got {
		if title == "Speed Racer" {
			t.Fatal("phantom positive not dropped: fully-watched-never-played show leaked through")
		}
	}
	for w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing expected row %q in %v", w, got)
		}
	}
}

func TestListSectionShowUnwatchedDoesNotSendServerFilter(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/sections/3/all", metaResp())

	library.ListSection("3", "show", true, "")

	q := f.lastQuery("/library/sections/3/all")
	if q.Has("unwatched") {
		t.Fatalf("shows must never send unwatched=1 (PMS's filter is wrong for shows), got %v", q)
	}
}

func TestListSectionMovieUnwatchedSendsServerFilter(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/sections/1/all", metaResp())

	library.ListSection("1", "movie", true, "")

	q := f.lastQuery("/library/sections/1/all")
	if q.Get("unwatched") != "1" {
		t.Fatalf("movies must send unwatched=1, got %v", q)
	}
}

func TestListSectionMovieRowsFilteredOnViewCount(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/sections/1/all", metaResp(
		movieItem("10", "Seen", 2), movieItem("11", "Unseen", 0),
	))

	rows := library.ListSection("1", "movie", true, "")
	got := titles(rows)
	if len(got) != 1 || got[0] != "Unseen" {
		t.Fatalf("rows = %v, want [Unseen]", got)
	}
}

func TestListSectionUntypedListingFiltersPerRowType(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/sections/3/all", metaResp(
		showItem("1961", "Speed Racer", 26, 26, nil),
		movieItem("11", "Unseen", 0),
	))

	rows := library.ListSection("3", "", true, "")
	got := titles(rows)
	if len(got) != 1 || got[0] != "Unseen" {
		t.Fatalf("rows = %v, want [Unseen]", got)
	}
}

func TestListSectionShowRowsProjectLeafCountersAsInt(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/sections/3/all", metaResp(showItem("2002", "The Simpsons", 6, 2, intPtr(9))))

	rows := library.ListSection("3", "show", false, "")
	row := rows[0]
	leaf, ok1 := row["leafCount"].(int)
	viewed, ok2 := row["viewedLeafCount"].(int)
	unwatched, ok3 := row["unwatchedLeaves"].(int)
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("leaf counters must marshal as int, got %#v", row)
	}
	if leaf != 6 || viewed != 2 || unwatched != 4 {
		t.Fatalf("leaf=%d viewed=%d unwatched=%d, want 6/2/4", leaf, viewed, unwatched)
	}
}

func TestListSectionMovieRowsDoNotGrowLeafFields(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/sections/1/all", metaResp(movieItem("10", "M", 0)))

	rows := library.ListSection("1", "movie", false, "")
	if _, ok := rows[0]["unwatchedLeaves"]; ok {
		t.Fatalf("movie row grew unwatchedLeaves: %#v", rows[0])
	}
}

func TestListSectionMissingCountersTreatedAsWatchedNothing(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/sections/3/all", metaResp(jsonx.J{"ratingKey": "30", "title": "Bare", "type": "show"}))

	rows := library.ListSection("3", "show", true, "")
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want empty (bare show has nothing unwatched)", rows)
	}
}

func TestListSectionTypeAndSortParams(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/sections/3/all", metaResp())

	library.ListSection("3", "show", false, "titleSort")
	q := f.lastQuery("/library/sections/3/all")
	if q.Get("type") != "2" {
		t.Fatalf("type = %q, want 2 for show", q.Get("type"))
	}
	if q.Get("sort") != "titleSort" {
		t.Fatalf("sort = %q, want titleSort", q.Get("sort"))
	}
}

// --- metadata --------------------------------------------------------------

func TestMetadataReturnsFirstItem(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/metadata/37", metaResp(jsonx.J{"ratingKey": "37", "title": "12 Angry Men", "extra": "field"}))

	got := library.Metadata("37")
	if got["title"] != "12 Angry Men" || got["extra"] != "field" {
		t.Fatalf("got = %#v, want passthrough of raw item", got)
	}
}

func TestMetadataMissingReturnsEmpty(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/metadata/999", metaResp())

	got := library.Metadata("999")
	if len(got) != 0 {
		t.Fatalf("got = %#v, want empty map", got)
	}
}

// --- fill_durations ----------------------------------------------------------

func TestFillDurationsFillsOnlyMissingDurations(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/metadata/2", metaResp(jsonx.J{"ratingKey": "2", "duration": float64(9999), "year": float64(2001)}))

	rows := []jsonx.J{
		{"ratingKey": "1", "duration": float64(123), "year": float64(2000)}, // already has duration -> no fetch
		{"ratingKey": "2", "duration": nil, "year": nil},                    // missing -> filled
		{"ratingKey": "", "duration": nil},                                  // falsy ratingKey -> skipped
	}

	got := library.FillDurations(rows)

	if got[0]["duration"] != float64(123) {
		t.Fatalf("row 0 mutated: %#v", got[0])
	}
	if f.callCount("/library/metadata/1") != 0 {
		t.Fatal("row 0 already had duration; must not trigger a fetch")
	}
	if got[1]["duration"] != json.Number("9999") || got[1]["year"] != json.Number("2001") {
		t.Fatalf("row 1 = %#v, want filled duration/year", got[1])
	}
	if got[2]["duration"] != nil {
		t.Fatalf("row 2 (no ratingKey) must be left alone: %#v", got[2])
	}
}

func TestFillDurationsToleratesPerItemFailure(t *testing.T) {
	f := newFakePMS(t)
	f.onStatus("/library/metadata/1", 500)
	f.onJSON("/library/metadata/2", metaResp(jsonx.J{"ratingKey": "2", "duration": float64(555), "year": float64(2010)}))

	rows := []jsonx.J{
		{"ratingKey": "1", "duration": nil}, // fetch fails -> unchanged
		{"ratingKey": "2", "duration": nil}, // fetch succeeds -> filled
	}

	got := library.FillDurations(rows)

	if got[0]["duration"] != nil {
		t.Fatalf("row 0 should remain nil after best-effort failure: %#v", got[0])
	}
	if got[1]["duration"] != json.Number("555") {
		t.Fatalf("row 1 = %#v, want filled despite sibling failure", got[1])
	}
}

func TestFillDurationsNoTargetsReturnsRowsUnchanged(t *testing.T) {
	newFakePMS(t) // no routes registered — a fetch here would 404 and fail the test
	rows := []jsonx.J{{"ratingKey": "1", "duration": float64(5)}}
	got := library.FillDurations(rows)
	if len(got) != 1 || got[0]["duration"] != float64(5) {
		t.Fatalf("got = %#v, want untouched", got)
	}
}

// --- scrobble / unscrobble / rate ---------------------------------------------

func okTrue(t *testing.T, got jsonx.J) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("got = %#v, want exactly {\"ok\": true}", got)
	}
	if v, ok := got["ok"].(bool); !ok || !v {
		t.Fatalf("got = %#v, want ok:true", got)
	}
}

func TestScrobbleSendsKeyAndIdentifier(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/:/scrobble", jsonx.J{})

	got := library.Scrobble("55")
	okTrue(t, got)

	q := f.lastQuery("/:/scrobble")
	if q.Get("key") != "55" || q.Get("identifier") != "com.plexapp.plugins.library" {
		t.Fatalf("params = %v, want key=55&identifier=com.plexapp.plugins.library", q)
	}
}

func TestUnscrobbleSendsKeyAndIdentifier(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/:/unscrobble", jsonx.J{})

	got := library.Unscrobble("55")
	okTrue(t, got)

	q := f.lastQuery("/:/unscrobble")
	if q.Get("key") != "55" || q.Get("identifier") != "com.plexapp.plugins.library" {
		t.Fatalf("params = %v, want key=55&identifier=com.plexapp.plugins.library", q)
	}
}

func TestRateSendsKeyIdentifierAndRating(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/:/rate", jsonx.J{})

	got := library.Rate("55", 8)
	okTrue(t, got)

	q := f.lastQuery("/:/rate")
	if q.Get("key") != "55" || q.Get("identifier") != "com.plexapp.plugins.library" || q.Get("rating") != "8" {
		t.Fatalf("params = %v, want key=55&identifier=com.plexapp.plugins.library&rating=8", q)
	}
}
