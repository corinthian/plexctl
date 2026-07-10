package collections

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/testutil"
)

// --- fake PMS harness --------------------------------------------------------

type capturedReq struct {
	method string
	path   string
	query  url.Values
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
		f.calls = append(f.calls, capturedReq{method: r.Method, path: r.URL.Path, query: r.URL.Query()})
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

func (f *fakePMS) methodCallCount(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.path == path && c.method == method {
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

func mc(items ...jsonx.J) jsonx.J {
	return jsonx.J{"MediaContainer": jsonx.J{"Metadata": anyList(items...)}}
}

func dirsResp(dirs ...jsonx.J) jsonx.J {
	return jsonx.J{"MediaContainer": jsonx.J{"Directory": anyList(dirs...)}}
}

func rootResp(machineID string) jsonx.J {
	return jsonx.J{"MediaContainer": jsonx.J{"machineIdentifier": machineID}}
}

func titles(rows []jsonx.J) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i], _ = r["title"].(string)
	}
	return out
}

// routeServerAndSections stubs the two calls every mutation path resolves
// through: GET / for the server machineIdentifier, GET /library/sections for
// the section-type lookup.
func routeServerAndSections(f *fakePMS, sections ...jsonx.J) {
	f.onJSON("/", rootResp("MID"))
	f.onJSON("/library/sections", dirsResp(sections...))
}

var fakeSections = []jsonx.J{
	{"key": "1", "title": "Movies", "type": "movie"},
	{"key": "2", "title": "TV", "type": "show"},
	{"key": "9", "title": "Music", "type": "artist"},
}

// notSmart routes the isSmart probe (GET /library/metadata/<key>) to a
// manual (non-smart) record.
func notSmart(f *fakePMS, key string) {
	f.onJSON("/library/metadata/"+key, mc(jsonx.J{"smart": false}))
}

// asSmart routes the isSmart probe to a smart record.
func asSmart(f *fakePMS, key string) {
	f.onJSON("/library/metadata/"+key, mc(jsonx.J{"smart": true}))
}

// --- list_all -----------------------------------------------------------------

func TestListAllWithSectionIDHitsSingleEndpoint(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/sections/2/collections", mc(
		jsonx.J{"ratingKey": "10", "title": "Marvel", "childCount": float64(12), "smart": false},
		jsonx.J{"ratingKey": "11", "title": "Psycho", "childCount": float64(8), "smart": true},
	))

	rows := ListAll("2")

	if got := titles(rows); len(got) != 2 || got[0] != "Marvel" || got[1] != "Psycho" {
		t.Fatalf("titles = %v", got)
	}
	if rows[0]["sectionID"] != "2" {
		t.Fatalf("sectionID = %#v, want \"2\"", rows[0]["sectionID"])
	}
	if rows[1]["smart"] != true {
		t.Fatalf("smart = %#v, want true", rows[1]["smart"])
	}
	if f.callCount("/library/sections") != 0 {
		t.Fatal("section_id given: must not walk library.Sections()")
	}
}

func TestListAllWithoutSectionWalksVideoSectionsOnly(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/sections", dirsResp(
		jsonx.J{"key": "1", "title": "Movies", "type": "movie"},
		jsonx.J{"key": "2", "title": "TV", "type": "show"},
		jsonx.J{"key": "3", "title": "Music", "type": "artist"},
	))
	f.onJSON("/library/sections/1/collections", mc(jsonx.J{"ratingKey": "100", "title": "Comfort", "childCount": float64(5)}))
	f.onJSON("/library/sections/2/collections", mc(jsonx.J{"ratingKey": "200", "title": "Twilight Zone", "childCount": float64(3)}))

	rows := ListAll("")

	if got := titles(rows); len(got) != 2 || got[0] != "Comfort" || got[1] != "Twilight Zone" {
		t.Fatalf("titles = %v", got)
	}
	if rows[0]["sectionID"] != "1" || rows[1]["sectionID"] != "2" {
		t.Fatalf("sectionIDs = %#v / %#v", rows[0]["sectionID"], rows[1]["sectionID"])
	}
	if f.callCount("/library/sections/3/collections") != 0 {
		t.Fatal("music (artist) section must be skipped")
	}
}

func TestListAllEmptySectionReturnsEmptyNonNilSlice(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/sections/2/collections", mc())

	rows := ListAll("2")
	if rows == nil || len(rows) != 0 {
		t.Fatalf("rows = %#v, want non-nil empty", rows)
	}
	if jsonx.Marshal(rows) != "[]" {
		t.Fatalf("Marshal = %s, want []", jsonx.Marshal(rows))
	}
}

func TestListAllHandlesMissingMetadataArray(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/sections/2/collections", jsonx.J{"MediaContainer": jsonx.J{}})

	rows := ListAll("2")
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want empty", rows)
	}
}

// --- show: smart resolution -----------------------------------------------

func TestShowReturnsChildItemsForManualCollection(t *testing.T) {
	f := newFakePMS(t)
	notSmart(f, "10")
	f.onJSON("/library/metadata/10/children", mc(
		jsonx.J{"ratingKey": "501", "title": "Rocky", "type": "movie", "year": float64(2021), "viewCount": float64(1)},
		jsonx.J{"ratingKey": "502", "title": "Rocky II", "type": "movie", "year": float64(2024)},
	))

	rows := Show("10", false)
	if got := titles(rows); len(got) != 2 || got[0] != "Rocky" || got[1] != "Rocky II" {
		t.Fatalf("titles = %v", got)
	}
	if rows[1]["viewCount"] != 0 {
		t.Fatalf("viewCount default = %#v, want 0 (absent key)", rows[1]["viewCount"])
	}
}

func TestShowEmptyCollectionReturnsEmptySlice(t *testing.T) {
	f := newFakePMS(t)
	notSmart(f, "10")
	f.onJSON("/library/metadata/10/children", mc())

	rows := Show("10", false)
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want empty", rows)
	}
}

func TestShowManualCollectionFallsThroughToChildren(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/metadata/10", mc(jsonx.J{"title": "Manual", "smart": false}))
	f.onJSON("/library/metadata/10/children", mc(
		jsonx.J{"ratingKey": "501", "title": "Rocky", "type": "movie", "year": float64(2021), "duration": float64(9090000)},
	))

	rows := Show("10", false)
	if f.callCount("/library/metadata/10") != 1 {
		t.Fatalf("metadata probe called %d times, want 1", f.callCount("/library/metadata/10"))
	}
	if f.callCount("/library/metadata/10/children") != 1 {
		t.Fatal("expected exactly one /children call")
	}
	if got := titles(rows); len(got) != 1 || got[0] != "Rocky" {
		t.Fatalf("titles = %v", got)
	}
	if rows[0]["duration"] != json.Number("9090000") {
		t.Fatalf("duration = %#v, want 9090000 (already present, no enrichment)", rows[0]["duration"])
	}
}

func TestShowSmartCollectionFollowsContentURI(t *testing.T) {
	f := newFakePMS(t)
	content := "server://abc/com.plexapp.plugins.library/library/sections/1/all?type=1&sort=titleSort&lastViewedAt%3E%3E=-4d"
	expectedRawQuery := "type=1&sort=titleSort&lastViewedAt%3E%3E=-4d"
	f.onJSON("/library/metadata/10", mc(jsonx.J{"smart": "1", "content": content}))
	f.on("/library/sections/1/all", func(r *http.Request) (int, any) {
		if r.URL.RawQuery != expectedRawQuery {
			t.Fatalf("raw query = %q, want %q", r.URL.RawQuery, expectedRawQuery)
		}
		return 200, mc(
			jsonx.J{"ratingKey": "9001", "title": "Recently Watched 1", "type": "movie", "year": float64(2020), "duration": float64(7200000)},
			jsonx.J{"ratingKey": "9002", "title": "Recently Watched 2", "type": "movie", "year": float64(2021), "duration": float64(8100000)},
		)
	})

	rows := Show("10", false)
	if got := titles(rows); len(got) != 2 || got[0] != "Recently Watched 1" || got[1] != "Recently Watched 2" {
		t.Fatalf("titles = %v", got)
	}
	if rows[0]["duration"] != json.Number("7200000") || rows[1]["duration"] != json.Number("8100000") {
		t.Fatalf("durations = %#v / %#v", rows[0]["duration"], rows[1]["duration"])
	}
	if f.callCount("/library/metadata/10/children") != 0 {
		t.Fatal("smart resolution succeeded: must not fall through to /children")
	}
}

func TestShowSmartCollectionMalformedContentFallsBackToChildren(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/metadata/10", mc(jsonx.J{"smart": "1", "content": "not a real uri"}))
	f.onJSON("/library/metadata/10/children", mc())

	rows := Show("10", false)
	if f.callCount("/library/metadata/10/children") != 1 {
		t.Fatal("marker-absent content must fall back to /children")
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want empty", rows)
	}
}

func TestShowRawSkipsSmartResolution(t *testing.T) {
	f := newFakePMS(t)
	// No /library/metadata/10 route registered — raw=true must never probe it.
	f.onJSON("/library/metadata/10/children", mc(
		jsonx.J{"ratingKey": "501", "title": "Rocky", "type": "movie", "year": float64(2021), "duration": float64(9090000)},
	))

	rows := Show("10", true)
	if f.callCount("/library/metadata/10") != 0 {
		t.Fatal("raw=true must not hit the metadata probe")
	}
	if got := titles(rows); len(got) != 1 || got[0] != "Rocky" {
		t.Fatalf("titles = %v", got)
	}
}

func TestShowManualCollectionEnrichesMissingDuration(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/metadata/10", mc(jsonx.J{"title": "Manual", "smart": false}))
	f.onJSON("/library/metadata/10/children", mc(jsonx.J{"ratingKey": "501", "title": "Rocky", "type": "movie", "year": float64(2021)}))
	f.onJSON("/library/metadata/501", mc(jsonx.J{"ratingKey": "501", "duration": float64(9090000), "year": float64(2021)}))

	rows := Show("10", false)
	if rows[0]["duration"] != json.Number("9090000") {
		t.Fatalf("duration = %#v, want enriched 9090000", rows[0]["duration"])
	}
}

func TestShowMetadataLookupFailureTreatedAsNoRecord(t *testing.T) {
	f := newFakePMS(t)
	f.onStatus("/library/metadata/10", 500)
	f.onJSON("/library/metadata/10/children", mc())

	rows := Show("10", false)
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want empty", rows)
	}
	if f.callCount("/library/metadata/10/children") != 1 {
		t.Fatal("metadata lookup failure must still fall through to /children")
	}
}

// --- smartContentPath -----------------------------------------------------

func TestSmartContentPathExtractsQueryPortion(t *testing.T) {
	content := "server://deadbeefdeadbeefdeadbeefdeadbeefdeadbeef/com.plexapp.plugins.library/library/sections/1/all?type=1&sort=titleSort&lastViewedAt%3E%3E=-4d"
	want := "/library/sections/1/all?type=1&sort=titleSort&lastViewedAt%3E%3E=-4d"
	if got := smartContentPath(content); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSmartContentPathReturnsEmptyForMissingMarker(t *testing.T) {
	if got := smartContentPath(nil); got != "" {
		t.Fatalf("nil -> %q, want \"\"", got)
	}
	if got := smartContentPath(""); got != "" {
		t.Fatalf("empty -> %q, want \"\"", got)
	}
	if got := smartContentPath("garbage with no marker"); got != "" {
		t.Fatalf("garbage -> %q, want \"\"", got)
	}
}

// --- mutations: create ------------------------------------------------------

func TestCreateEmptyKeysReturnsError(t *testing.T) {
	got := Create("X", "1", nil)
	want := jsonx.J{"ok": false, "error": "create requires at least one ratingKey"}
	if got["ok"] != want["ok"] || got["error"] != want["error"] {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestCreateNonVideoSectionRejected(t *testing.T) {
	f := newFakePMS(t)
	routeServerAndSections(f, fakeSections...)

	result := Create("X", "9", []string{"100"})
	want := "section 9 is not a movie or show section"
	if result["ok"] != false || result["error"] != want {
		t.Fatalf("got %#v, want error %q", result, want)
	}
}

func TestCreateUnknownSectionRejected(t *testing.T) {
	f := newFakePMS(t)
	routeServerAndSections(f, fakeSections...)

	result := Create("X", "99", []string{"100"})
	if result["ok"] != false {
		t.Fatalf("ok = %#v, want false", result["ok"])
	}
}

func TestCreateNoMetadataReturnsExactError(t *testing.T) {
	f := newFakePMS(t)
	routeServerAndSections(f, fakeSections...)
	f.onJSON("/library/collections", mc()) // POST returns no Metadata items

	result := Create("X", "1", []string{"100"})
	want := "collection creation returned no metadata"
	if result["ok"] != false || result["error"] != want {
		t.Fatalf("got %#v, want error %q", result, want)
	}
}

func TestCreateNoRatingKeyReturnsExactError(t *testing.T) {
	f := newFakePMS(t)
	routeServerAndSections(f, fakeSections...)
	f.onJSON("/library/collections", mc(jsonx.J{"title": "Comfort"})) // item, no ratingKey

	result := Create("X", "1", []string{"100"})
	want := "collection creation returned no ratingKey"
	if result["ok"] != false || result["error"] != want {
		t.Fatalf("got %#v, want error %q", result, want)
	}
}

func TestCreateNoServerIDRejected(t *testing.T) {
	f := newFakePMS(t)
	f.onStatus("/", 500) // GetServerMachineID -> ""

	result := Create("X", "1", []string{"100"})
	want := "could not retrieve server machineIdentifier"
	if result["ok"] != false || result["error"] != want {
		t.Fatalf("got %#v", result)
	}
}

func TestCreateSingleKeyPostsAndReturnsRatingKey(t *testing.T) {
	f := newFakePMS(t)
	routeServerAndSections(f, fakeSections...)
	f.onJSON("/library/collections", mc(jsonx.J{"ratingKey": "777", "title": "Comfort"}))

	result := Create("Comfort", "1", []string{"100"})
	want := jsonx.J{"ok": true, "ratingKey": "777", "title": "Comfort", "count": 1}
	if result["ok"] != want["ok"] || result["ratingKey"] != want["ratingKey"] ||
		result["title"] != want["title"] || result["count"] != want["count"] {
		t.Fatalf("got %#v, want %#v", result, want)
	}

	q := f.lastQuery("/library/collections")
	if q.Get("title") != "Comfort" || q.Get("sectionId") != "1" || q.Get("type") != "1" || q.Get("smart") != "0" {
		t.Fatalf("post params = %v", q)
	}
	if !strings.HasSuffix(q.Get("uri"), "/library/metadata/100") {
		t.Fatalf("uri = %q", q.Get("uri"))
	}
	if f.methodCallCount("PUT", "/library/collections/777/items") != 0 {
		t.Fatal("single-key create must not PUT any items")
	}
}

func TestCreateMultipleKeysAddsRemainingViaPut(t *testing.T) {
	f := newFakePMS(t)
	routeServerAndSections(f, fakeSections...)
	f.onJSON("/library/collections", mc(jsonx.J{"ratingKey": "777", "title": "Comfort"}))
	f.onJSON("/library/collections/777/items", jsonx.J{})

	result := Create("Comfort", "1", []string{"100", "101", "102"})
	if result["ok"] != true || result["count"] != 3 {
		t.Fatalf("got %#v", result)
	}
	if got := f.methodCallCount("PUT", "/library/collections/777/items"); got != 2 {
		t.Fatalf("PUT count = %d, want 2 (2nd and 3rd keys only)", got)
	}
}

// TestCreateRollsBackOnPartialAddFailure pins W11: the mid-loop add now
// goes through api.TryPut, so a real HTTP failure on the second item's PUT
// — not a monkeypatched addItemsFn — reaches the ok:false branch and
// triggers rollback. Before W11 this was reachable only via the
// now-removed test-only seam: api.Put print-and-exits, so this exact
// failure used to kill the test process instead of returning ok:false.
func TestCreateRollsBackOnPartialAddFailure(t *testing.T) {
	f := newFakePMS(t)
	routeServerAndSections(f, fakeSections...)
	f.onJSON("/library/collections", mc(jsonx.J{"ratingKey": "777", "title": "Comfort"}))
	f.onStatus("/library/collections/777/items", 500)
	f.onJSON("/library/metadata/777", jsonx.J{}) // TryDelete target

	result := Create("Comfort", "1", []string{"100", "101"})
	if result["ok"] != false {
		t.Fatalf("ok = %#v, want false", result["ok"])
	}
	if result["partialCollectionID"] != "777" {
		t.Fatalf("partialCollectionID = %#v, want 777", result["partialCollectionID"])
	}
	if result["rollbackAttempted"] != true {
		t.Fatalf("rollbackAttempted = %#v, want true", result["rollbackAttempted"])
	}
	if f.methodCallCount("DELETE", "/library/metadata/777") != 1 {
		t.Fatal("rollback must DELETE /library/metadata/777 exactly once")
	}
}

// TestCreateRollsBackOnTransportFailure covers the failure mode the D2
// spec called out specifically: a timeout/connection failure mid-loop,
// not just an HTTP error status.
func TestCreateRollsBackOnTransportFailure(t *testing.T) {
	f := newFakePMS(t)
	routeServerAndSections(f, fakeSections...)
	f.onJSON("/library/collections", mc(jsonx.J{"ratingKey": "777", "title": "Comfort"}))
	f.on("/library/collections/777/items", func(r *http.Request) (int, any) {
		panic(http.ErrAbortHandler) // aborts the connection: classifies as a transport error
	})
	f.onJSON("/library/metadata/777", jsonx.J{}) // TryDelete target

	result := Create("Comfort", "1", []string{"100", "101"})
	if result["ok"] != false {
		t.Fatalf("ok = %#v, want false", result["ok"])
	}
	if result["rollbackAttempted"] != true {
		t.Fatalf("rollbackAttempted = %#v, want true", result["rollbackAttempted"])
	}
	if f.methodCallCount("DELETE", "/library/metadata/777") != 1 {
		t.Fatal("rollback must DELETE /library/metadata/777 exactly once")
	}
}

// --- mutations: delete / rename ---------------------------------------------

func TestDeleteHitsLibraryMetadata(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/library/metadata/777", jsonx.J{})

	got := Delete("777")
	if got["ok"] != true {
		t.Fatalf("got %#v", got)
	}
	if f.methodCallCount("DELETE", "/library/metadata/777") != 1 {
		t.Fatal("expected exactly one DELETE to /library/metadata/777")
	}
}

func TestRenameLocksTitle(t *testing.T) {
	f := newFakePMS(t)

	// The isSmart probe and the rename PUT hit the same path with different
	// methods -- the harness must dispatch on method within a single route.
	var putSeen bool
	f.on("/library/metadata/777", func(r *http.Request) (int, any) {
		if r.Method == http.MethodPut {
			putSeen = true
			if r.URL.Query().Get("title.value") != "New Name" || r.URL.Query().Get("title.locked") != "1" {
				t.Fatalf("PUT params = %v", r.URL.Query())
			}
			return 200, jsonx.J{}
		}
		return 200, mc(jsonx.J{"smart": false})
	})

	got := Rename("777", "New Name")
	if got["ok"] != true {
		t.Fatalf("got %#v", got)
	}
	if !putSeen {
		t.Fatal("expected a PUT to /library/metadata/777")
	}
}

// --- mutations: add / remove -------------------------------------------------

func TestAddItemsLoopsPerKey(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/", rootResp("MID"))
	notSmart(f, "777")
	f.onJSON("/library/collections/777/items", jsonx.J{})

	result := AddItems("777", []string{"100", "101", "102"})
	want := jsonx.J{"ok": true, "added": 3}
	if result["ok"] != want["ok"] || result["added"] != want["added"] {
		t.Fatalf("got %#v", result)
	}
	if got := f.methodCallCount("PUT", "/library/collections/777/items"); got != 3 {
		t.Fatalf("PUT count = %d, want 3", got)
	}
}

func TestAddItemsEmptyRejected(t *testing.T) {
	got := AddItems("777", nil)
	if got["ok"] != false || got["error"] != "add requires at least one ratingKey" {
		t.Fatalf("got %#v", got)
	}
}

func TestRemoveItemUsesDelete(t *testing.T) {
	f := newFakePMS(t)
	notSmart(f, "777")
	f.onJSON("/library/collections/777/items/100", jsonx.J{})

	got := RemoveItem("777", "100")
	if got["ok"] != true {
		t.Fatalf("got %#v", got)
	}
	if f.methodCallCount("DELETE", "/library/collections/777/items/100") != 1 {
		t.Fatal("PMS 1.43 wants DELETE here, not PUT")
	}
}

// --- smart-collection refusal ------------------------------------------------

// wantSmartRefusal pins the exact smart-refusal wording independently of the
// Go smartRefusal constant under test, so the assertion can't pass on a
// self-referential typo.
const wantSmartRefusal = "smart collection: contents are query-driven and cannot be edited via " +
	"the API — edit the smart filter in the Plex app instead"

func TestAddItemsRefusesSmartCollection(t *testing.T) {
	f := newFakePMS(t)
	asSmart(f, "777")

	result := AddItems("777", []string{"100"})
	if result["ok"] != false || result["error"] != wantSmartRefusal {
		t.Fatalf("got %#v", result)
	}
	if f.callCount("/library/collections/777/items") != 0 {
		t.Fatal("no PUT should fire when refused")
	}
}

func TestRemoveItemRefusesSmartCollection(t *testing.T) {
	f := newFakePMS(t)
	asSmart(f, "777")

	result := RemoveItem("777", "100")
	if result["ok"] != false || result["error"] != wantSmartRefusal {
		t.Fatalf("got %#v", result)
	}
	if f.callCount("/library/collections/777/items/100") != 0 {
		t.Fatal("no DELETE should fire when refused")
	}
}

func TestRenameRefusesSmartCollection(t *testing.T) {
	f := newFakePMS(t)
	asSmart(f, "777")

	result := Rename("777", "New")
	if result["ok"] != false || result["error"] != wantSmartRefusal {
		t.Fatalf("got %#v", result)
	}
	if f.methodCallCount("PUT", "/library/metadata/777") != 0 {
		t.Fatal("no PUT should fire when refused")
	}
}

func TestDeleteAllowedOnSmartCollection(t *testing.T) {
	f := newFakePMS(t)
	asSmart(f, "777") // isSmart is never consulted by Delete
	f.onJSON("/library/metadata/777", jsonx.J{})

	got := Delete("777")
	if got["ok"] != true {
		t.Fatalf("got %#v", got)
	}
}

func TestCreateLoopSkipsSmartCheck(t *testing.T) {
	f := newFakePMS(t)
	routeServerAndSections(f, fakeSections...)
	f.onJSON("/library/collections", mc(jsonx.J{"ratingKey": "777", "title": "Comfort"}))
	// If the create loop consulted isSmart, this route would say smart:true
	// and the add would be refused.
	asSmart(f, "777")
	f.onJSON("/library/collections/777/items", jsonx.J{})

	result := Create("Comfort", "1", []string{"100", "101"})
	if result["ok"] != true {
		t.Fatalf("got %#v, want ok:true (trustManual must bypass isSmart)", result)
	}
	if f.methodCallCount("PUT", "/library/collections/777/items") != 1 {
		t.Fatal("expected exactly one PUT for the second key")
	}
}
