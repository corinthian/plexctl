package playlists

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// notSmart routes the isSmart probe (GET /playlists/<key>) to a manual
// (non-smart) record.
func notSmart(f *fakePMS, key string) {
	f.onJSON("/playlists/"+key, mc(jsonx.J{"smart": false}))
}

// asSmart routes the isSmart probe to a smart record.
func asSmart(f *fakePMS, key string) {
	f.onJSON("/playlists/"+key, mc(jsonx.J{"smart": true}))
}

// --- list_all -----------------------------------------------------------------

func TestListAllNoFilterOmitsPlaylistTypeParam(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/playlists", mc(
		jsonx.J{"ratingKey": "20", "title": "Comfort Movies", "playlistType": "video", "smart": false, "leafCount": float64(7), "duration": float64(12345)},
		jsonx.J{"ratingKey": "21", "title": "Workout", "playlistType": "audio", "smart": false, "leafCount": float64(30)},
	))

	rows, err := ListAll("")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := titles(rows); len(got) != 2 || got[0] != "Comfort Movies" || got[1] != "Workout" {
		t.Fatalf("titles = %v", got)
	}
	if rows[1]["duration"] != nil {
		t.Fatalf("duration = %#v, want nil (missing key)", rows[1]["duration"])
	}
	if f.lastQuery("/playlists").Has("playlistType") {
		t.Fatal("no filter given: playlistType param must be omitted")
	}
}

func TestListAllWithVideoTypeSendsParam(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/playlists", mc())

	if _, err := ListAll("video"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if got := f.lastQuery("/playlists").Get("playlistType"); got != "video" {
		t.Fatalf("playlistType = %q, want video", got)
	}
}

func TestListAllInvalidTypeReturnsExactError(t *testing.T) {
	rows, err := ListAll("smartlist")
	if rows != nil {
		t.Fatalf("rows = %#v, want nil", rows)
	}
	if err == nil || err.Error() != "playlist_type must be one of ['audio', 'photo', 'video']" {
		t.Fatalf("err = %v", err)
	}
}

func TestListAllEmptyReturnsEmptyNonNilSlice(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/playlists", mc())

	rows, err := ListAll("")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("rows = %#v, want non-nil empty", rows)
	}
	if jsonx.Marshal(rows) != "[]" {
		t.Fatalf("Marshal = %s, want []", jsonx.Marshal(rows))
	}
}

// --- show --------------------------------------------------------------------

func TestShowReturnsItems(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/playlists/20/items", mc(
		jsonx.J{"playlistItemID": "5001", "ratingKey": "601", "title": "Rocky", "type": "movie", "year": float64(2021), "viewCount": float64(1)},
		jsonx.J{"playlistItemID": "5002", "ratingKey": "602", "title": "Alien", "type": "movie", "year": float64(2016)},
	))

	rows := Show("20")
	if got := titles(rows); len(got) != 2 || got[0] != "Rocky" || got[1] != "Alien" {
		t.Fatalf("titles = %v", got)
	}
	if rows[0]["playlistItemID"] != "5001" {
		t.Fatalf("playlistItemID = %#v, want 5001", rows[0]["playlistItemID"])
	}
	if rows[1]["viewCount"] != 0 {
		t.Fatalf("viewCount default = %#v, want 0", rows[1]["viewCount"])
	}
}

func TestShowEmptyPlaylistReturnsEmptySlice(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/playlists/20/items", mc())

	rows := Show("20")
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want empty", rows)
	}
}

// --- mutations: create ------------------------------------------------------

func TestCreateEmptyKeysRejected(t *testing.T) {
	got := Create("X", "video", nil)
	if got["ok"] != false || got["error"] != "create requires at least one ratingKey" {
		t.Fatalf("got %#v", got)
	}
}

// wantInvalidPlaylistTypeMsg is copied verbatim from the Python
// f"playlist_type must be one of {sorted(_VALID_TYPES)}" rendering (not the
// Go invalidPlaylistTypeMsg constant under test) so the assertion can't pass
// on a self-referential typo.
const wantInvalidPlaylistTypeMsg = "playlist_type must be one of ['audio', 'photo', 'video']"

// wantSmartRefusal pins the exact smart-refusal wording independently of the
// Go smartRefusal constant under test.
const wantSmartRefusal = "smart playlist: contents are query-driven and cannot be edited via " +
	"the API — edit the smart filter in the Plex app instead"

func TestCreateInvalidTypeRejected(t *testing.T) {
	result := Create("X", "smartlist", []string{"100"})
	if result["ok"] != false || result["error"] != wantInvalidPlaylistTypeMsg {
		t.Fatalf("got %#v, want error %q", result, wantInvalidPlaylistTypeMsg)
	}
}

func TestCreateNoMetadataReturnsExactError(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/", rootResp("MID"))
	f.onJSON("/playlists", mc()) // POST returns no Metadata items

	result := Create("X", "video", []string{"100"})
	want := "playlist creation returned no metadata"
	if result["ok"] != false || result["error"] != want {
		t.Fatalf("got %#v, want error %q", result, want)
	}
}

func TestCreateNoRatingKeyReturnsExactError(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/", rootResp("MID"))
	f.onJSON("/playlists", mc(jsonx.J{"title": "Comfort"})) // item, no ratingKey

	result := Create("X", "video", []string{"100"})
	want := "playlist creation returned no ratingKey"
	if result["ok"] != false || result["error"] != want {
		t.Fatalf("got %#v, want error %q", result, want)
	}
}

func TestCreateNoServerIDRejected(t *testing.T) {
	f := newFakePMS(t)
	f.onStatus("/", 500)

	result := Create("X", "video", []string{"100"})
	want := "could not retrieve server machineIdentifier"
	if result["ok"] != false || result["error"] != want {
		t.Fatalf("got %#v", result)
	}
}

func TestCreateSingleKeyPosts(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/", rootResp("MID"))
	f.onJSON("/playlists", mc(jsonx.J{"ratingKey": "888", "title": "Comfort"}))

	result := Create("Comfort", "video", []string{"100"})
	if result["ok"] != true || result["ratingKey"] != "888" || result["title"] != "Comfort" || result["count"] != 1 {
		t.Fatalf("got %#v", result)
	}

	q := f.lastQuery("/playlists")
	if q.Get("type") != "video" || q.Get("smart") != "0" || q.Get("title") != "Comfort" {
		t.Fatalf("post params = %v", q)
	}
	if got := q.Get("uri"); len(got) < len("/library/metadata/100") || got[len(got)-len("/library/metadata/100"):] != "/library/metadata/100" {
		t.Fatalf("uri = %q", got)
	}
	if f.methodCallCount("PUT", "/playlists/888/items") != 0 {
		t.Fatal("single-key create must not PUT any items")
	}
}

func TestCreateMultipleKeysLoopsAdds(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/", rootResp("MID"))
	f.onJSON("/playlists", mc(jsonx.J{"ratingKey": "888"}))
	f.onJSON("/playlists/888/items", jsonx.J{})

	result := Create("Comfort", "video", []string{"100", "101", "102"})
	if result["ok"] != true || result["count"] != 3 {
		t.Fatalf("got %#v", result)
	}
	if got := f.methodCallCount("PUT", "/playlists/888/items"); got != 2 {
		t.Fatalf("PUT count = %d, want 2", got)
	}
}

func TestCreateRollsBackOnPartialAddFailure(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/", rootResp("MID"))
	f.onJSON("/playlists", mc(jsonx.J{"ratingKey": "888"}))
	f.onJSON("/playlists/888", jsonx.J{}) // TryDelete target

	orig := addItemsFn
	addItemsFn = func(key string, keys []string, serverID string, trustManual bool) jsonx.J {
		return jsonx.J{"ok": false, "error": "boom"}
	}
	defer func() { addItemsFn = orig }()

	result := Create("Comfort", "video", []string{"100", "101"})
	if result["ok"] != false {
		t.Fatalf("ok = %#v, want false", result["ok"])
	}
	if result["partialPlaylistID"] != "888" {
		t.Fatalf("partialPlaylistID = %#v, want 888", result["partialPlaylistID"])
	}
	if result["rollbackAttempted"] != true {
		t.Fatalf("rollbackAttempted = %#v, want true", result["rollbackAttempted"])
	}
	if f.methodCallCount("DELETE", "/playlists/888") != 1 {
		t.Fatal("rollback must DELETE /playlists/888 exactly once")
	}
}

// --- mutations: delete / rename ----------------------------------------------

func TestDeleteHitsPlaylistPath(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/playlists/888", jsonx.J{})

	got := Delete("888")
	if got["ok"] != true {
		t.Fatalf("got %#v", got)
	}
	if f.methodCallCount("DELETE", "/playlists/888") != 1 {
		t.Fatal("expected exactly one DELETE to /playlists/888")
	}
}

func TestRenameSendsTitleParam(t *testing.T) {
	f := newFakePMS(t)
	var putSeen bool
	f.on("/playlists/888", func(r *http.Request) (int, any) {
		if r.Method == http.MethodPut {
			putSeen = true
			if r.URL.Query().Get("title") != "Cozy Movies" {
				t.Fatalf("PUT params = %v", r.URL.Query())
			}
			if len(r.URL.Query()) != 1 {
				t.Fatalf("PUT params = %v, want only title", r.URL.Query())
			}
			return 200, jsonx.J{}
		}
		return 200, mc(jsonx.J{"smart": false})
	})

	got := Rename("888", "Cozy Movies")
	if got["ok"] != true {
		t.Fatalf("got %#v", got)
	}
	if !putSeen {
		t.Fatal("expected a PUT to /playlists/888")
	}
}

// --- mutations: add / remove / clear ------------------------------------------

func TestAddItemsLoopsPerKey(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/", rootResp("MID"))
	notSmart(f, "888")
	f.onJSON("/playlists/888/items", jsonx.J{})

	result := AddItems("888", []string{"100", "101"})
	if result["ok"] != true || result["added"] != 2 {
		t.Fatalf("got %#v", result)
	}
	if got := f.methodCallCount("PUT", "/playlists/888/items"); got != 2 {
		t.Fatalf("PUT count = %d, want 2", got)
	}
}

func TestAddItemsEmptyRejected(t *testing.T) {
	got := AddItems("888", nil)
	if got["ok"] != false || got["error"] != "add requires at least one ratingKey" {
		t.Fatalf("got %#v", got)
	}
}

func TestRemoveItemUsesDeleteByItemID(t *testing.T) {
	f := newFakePMS(t)
	notSmart(f, "888")
	f.onJSON("/playlists/888/items/5001", jsonx.J{})

	got := RemoveItem("888", "5001")
	if got["ok"] != true {
		t.Fatalf("got %#v", got)
	}
	if f.methodCallCount("DELETE", "/playlists/888/items/5001") != 1 {
		t.Fatal("expected exactly one DELETE to /playlists/888/items/5001")
	}
}

func TestClearDeletesItemsEndpoint(t *testing.T) {
	f := newFakePMS(t)
	notSmart(f, "888")
	f.onJSON("/playlists/888/items", jsonx.J{})

	got := Clear("888")
	if got["ok"] != true {
		t.Fatalf("got %#v", got)
	}
	if f.methodCallCount("DELETE", "/playlists/888/items") != 1 {
		t.Fatal("expected exactly one DELETE to /playlists/888/items")
	}
}

// --- smart-playlist refusal ---------------------------------------------------

func TestAddItemsRefusesSmartPlaylist(t *testing.T) {
	f := newFakePMS(t)
	asSmart(f, "888")

	result := AddItems("888", []string{"100"})
	if result["ok"] != false || result["error"] != wantSmartRefusal {
		t.Fatalf("got %#v", result)
	}
	if f.callCount("/playlists/888/items") != 0 {
		t.Fatal("no PUT should fire when refused")
	}
}

func TestRemoveItemRefusesSmartPlaylist(t *testing.T) {
	f := newFakePMS(t)
	asSmart(f, "888")

	result := RemoveItem("888", "5001")
	if result["ok"] != false || result["error"] != wantSmartRefusal {
		t.Fatalf("got %#v", result)
	}
	if f.callCount("/playlists/888/items/5001") != 0 {
		t.Fatal("no DELETE should fire when refused")
	}
}

func TestClearRefusesSmartPlaylist(t *testing.T) {
	f := newFakePMS(t)
	asSmart(f, "888")

	result := Clear("888")
	if result["ok"] != false || result["error"] != wantSmartRefusal {
		t.Fatalf("got %#v", result)
	}
	if f.callCount("/playlists/888/items") != 0 {
		t.Fatal("no DELETE should fire when refused")
	}
}

func TestRenameRefusesSmartPlaylist(t *testing.T) {
	f := newFakePMS(t)
	asSmart(f, "888")

	result := Rename("888", "New")
	if result["ok"] != false || result["error"] != wantSmartRefusal {
		t.Fatalf("got %#v", result)
	}
	if f.methodCallCount("PUT", "/playlists/888") != 0 {
		t.Fatal("no PUT should fire when refused")
	}
}

func TestDeleteAllowedOnSmartPlaylist(t *testing.T) {
	f := newFakePMS(t)
	asSmart(f, "888") // isSmart is never consulted by Delete
	f.onJSON("/playlists/888", jsonx.J{})

	got := Delete("888")
	if got["ok"] != true {
		t.Fatalf("got %#v", got)
	}
}

func TestCreateLoopSkipsSmartCheck(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("/", rootResp("MID"))
	f.onJSON("/playlists", mc(jsonx.J{"ratingKey": "888"}))
	// If the create loop consulted isSmart, this route would say smart:true
	// and the add would be refused.
	asSmart(f, "888")
	f.onJSON("/playlists/888/items", jsonx.J{})

	result := Create("Comfort", "video", []string{"100", "101"})
	if result["ok"] != true {
		t.Fatalf("got %#v, want ok:true (trustManual must bypass isSmart)", result)
	}
	if f.methodCallCount("PUT", "/playlists/888/items") != 1 {
		t.Fatal("expected exactly one PUT for the second key")
	}
}
