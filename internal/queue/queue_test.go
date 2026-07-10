package queue

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/queuestate"
	"github.com/corinthian/plexctl/internal/testutil"
)

// --- fake PMS harness --------------------------------------------------------
// Routes by method+path (queue.go PUTs and GETs the same /playQueues/{id}
// path, so a path-only table like library_test.go's isn't enough here).

type capturedReq struct {
	method string
	path   string
	query  url.Values
}

type fakePMS struct {
	mu     sync.Mutex
	calls  []capturedReq
	routes map[string]map[string]func(r *http.Request) (int, any)
	srv    *httptest.Server
}

func newFakePMS(t *testing.T) *fakePMS {
	t.Helper()
	f := &fakePMS{routes: map[string]map[string]func(r *http.Request) (int, any){}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, capturedReq{method: r.Method, path: r.URL.Path, query: r.URL.Query()})
		var handler func(r *http.Request) (int, any)
		if methods, ok := f.routes[r.URL.Path]; ok {
			handler = methods[r.Method]
		}
		f.mu.Unlock()
		if handler == nil {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
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
	f.srv = srv
	testutil.Setup(t, srv.URL)
	return f
}

func (f *fakePMS) on(method, path string, fn func(r *http.Request) (int, any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.routes[path] == nil {
		f.routes[path] = map[string]func(r *http.Request) (int, any){}
	}
	f.routes[path][method] = fn
}

func (f *fakePMS) onJSON(method, path string, body any) {
	f.on(method, path, func(r *http.Request) (int, any) { return 200, body })
}

func (f *fakePMS) onStatus(method, path string, status int) {
	f.on(method, path, func(r *http.Request) (int, any) { return status, nil })
}

// onSequence returns bodies[0], bodies[1], ... on successive calls, then
// stays on the last one (mirrors unittest.mock's side_effect list, which the
// Python tests use for stubbing successive api.get() reads).
func (f *fakePMS) onSequence(method, path string, bodies ...any) {
	var mu sync.Mutex
	idx := 0
	f.on(method, path, func(r *http.Request) (int, any) {
		mu.Lock()
		defer mu.Unlock()
		b := bodies[idx]
		if idx < len(bodies)-1 {
			idx++
		}
		return 200, b
	})
}

func (f *fakePMS) callCount(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.method == method && c.path == path {
			n++
		}
	}
	return n
}

func (f *fakePMS) queriesFor(method, path string) []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []url.Values
	for _, c := range f.calls {
		if c.method == method && c.path == path {
			out = append(out, c.query)
		}
	}
	return out
}

func (f *fakePMS) lastQuery(method, path string) url.Values {
	qs := f.queriesFor(method, path)
	if len(qs) == 0 {
		return nil
	}
	return qs[len(qs)-1]
}

// serverIDRoute registers the "/" machineIdentifier lookup that
// playback.GetServerMachineID depends on.
func (f *fakePMS) serverIDRoute(mid string) {
	f.onJSON("GET", "/", jsonx.J{"MediaContainer": jsonx.J{"machineIdentifier": mid}})
}

// --- fixtures ----------------------------------------------------------------

const serverMID = "SERVER-MID"

func appleTV() jsonx.J {
	return jsonx.J{"machineIdentifier": "abc", "name": "Apple TV", "baseurl": "http://x:32500"}
}

func sizeResp(size int) jsonx.J {
	return jsonx.J{"MediaContainer": jsonx.J{"size": size}}
}

func loadGolden(t *testing.T, name string) jsonx.J {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "goldens", name))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var v jsonx.J
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	return v
}

// normalize round-trips a result through JSON so map/slice dynamic types
// match what a golden file (or another JSON round trip) unmarshals to —
// jsonx values built in-process carry []jsonx.J, not []interface{}.
func normalize(t *testing.T, v jsonx.J) jsonx.J {
	t.Helper()
	var out jsonx.J
	if err := json.Unmarshal([]byte(jsonx.Marshal(v)), &out); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return out
}

// --- Create: continuous flag semantics ---------------------------------------

func TestCreateSingleKeySendsContinuous1(t *testing.T) {
	f := newFakePMS(t)
	f.serverIDRoute(serverMID)
	f.onJSON("POST", "/playQueues", jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "999", "playQueueSelectedItemID": "42"}})

	result := Create([]string{"100"}, false, false)

	if result["ok"] != true {
		t.Fatalf("result = %#v", result)
	}
	if result["playQueueID"] != "999" || result["selectedItemID"] != "42" {
		t.Fatalf("result = %#v", result)
	}
	q := f.lastQuery("POST", "/playQueues")
	if q.Get("continuous") != "1" {
		t.Fatalf("continuous = %q, want 1", q.Get("continuous"))
	}
	wantURI := "server://SERVER-MID/com.plexapp.plugins.library/library/metadata/100"
	if q.Get("uri") != wantURI {
		t.Fatalf("uri = %q, want %q", q.Get("uri"), wantURI)
	}
	if q.Get("type") != "video" {
		t.Fatalf("type = %q, want video", q.Get("type"))
	}
}

func TestCreateMultiKeySendsContinuous0AndAddsRemaining(t *testing.T) {
	f := newFakePMS(t)
	f.serverIDRoute(serverMID)
	f.onJSON("POST", "/playQueues", jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "999", "playQueueSelectedItemID": "42"}})
	f.onJSON("PUT", "/playQueues/999", jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "999"}})

	result := Create([]string{"100", "101", "102"}, false, false)

	if result["ok"] != true {
		t.Fatalf("result = %#v", result)
	}
	if q := f.lastQuery("POST", "/playQueues"); q.Get("continuous") != "0" {
		t.Fatalf("continuous = %q, want 0", q.Get("continuous"))
	}
	puts := f.queriesFor("PUT", "/playQueues/999")
	if len(puts) != 2 {
		t.Fatalf("PUT count = %d, want 2", len(puts))
	}
	wantURIs := []string{
		"server://SERVER-MID/com.plexapp.plugins.library/library/metadata/101",
		"server://SERVER-MID/com.plexapp.plugins.library/library/metadata/102",
	}
	for i, want := range wantURIs {
		if got := puts[i].Get("uri"); got != want {
			t.Fatalf("PUT[%d] uri = %q, want %q", i, got, want)
		}
	}
}

func TestCreateTwoKeyAlsoSendsContinuous0(t *testing.T) {
	f := newFakePMS(t)
	f.serverIDRoute(serverMID)
	f.onJSON("POST", "/playQueues", jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "999", "playQueueSelectedItemID": "42"}})
	f.onJSON("PUT", "/playQueues/999", jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "999"}})

	Create([]string{"100", "101"}, false, false)

	if q := f.lastQuery("POST", "/playQueues"); q.Get("continuous") != "0" {
		t.Fatalf("continuous = %q, want 0", q.Get("continuous"))
	}
}

func TestCreateContinuousDoesNotAffectShuffleRepeat(t *testing.T) {
	f := newFakePMS(t)
	f.serverIDRoute(serverMID)
	f.onJSON("POST", "/playQueues", jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "999", "playQueueSelectedItemID": "42"}})
	f.onJSON("PUT", "/playQueues/999", jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "999"}})

	Create([]string{"100", "101"}, true, true)

	q := f.lastQuery("POST", "/playQueues")
	if q.Get("shuffle") != "1" {
		t.Fatalf("shuffle = %q, want 1", q.Get("shuffle"))
	}
	if q.Get("repeat") != "1" {
		t.Fatalf("repeat = %q, want 1", q.Get("repeat"))
	}
	if q.Get("continuous") != "0" {
		t.Fatalf("continuous = %q, want 0", q.Get("continuous"))
	}
}

// --- Create: error branches ---------------------------------------------------

func TestCreateReturnsErrorWhenServerIDMissing(t *testing.T) {
	newFakePMS(t) // no "/" route registered -> 404 -> GetServerMachineID() == ""

	result := Create([]string{"100"}, false, false)

	want := jsonx.J{"ok": false, "error": "could not retrieve server machineIdentifier"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

func TestCreateMissingPlayQueueIDReturnsError(t *testing.T) {
	f := newFakePMS(t)
	f.serverIDRoute(serverMID)
	f.onJSON("POST", "/playQueues", jsonx.J{"MediaContainer": jsonx.J{}})

	result := Create([]string{"100"}, false, false)

	want := jsonx.J{"ok": false, "error": "playQueue creation returned no playQueueID"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

func TestCreateMissingSelectedItemIDReturnsError(t *testing.T) {
	f := newFakePMS(t)
	f.serverIDRoute(serverMID)
	f.onJSON("POST", "/playQueues", jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "999"}})

	result := Create([]string{"100"}, false, false)

	want := jsonx.J{"ok": false, "error": "playQueue created but PMS returned no playQueueSelectedItemID"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

func TestCreateMultiKeyMidLoopFailureRollsBack(t *testing.T) {
	f := newFakePMS(t)
	f.serverIDRoute(serverMID)
	f.onJSON("POST", "/playQueues", jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "999", "playQueueSelectedItemID": "42"}})
	// First PUT (key 101) succeeds; second (key 102) comes back without
	// playQueueID, which Add() treats as "unexpected response".
	f.onSequence("PUT", "/playQueues/999",
		jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "999"}},
		jsonx.J{"MediaContainer": jsonx.J{}},
	)
	// Rollback DELETE: verify it's attempted and its failure is swallowed
	// (never raises) by returning a server error.
	f.onStatus("DELETE", "/playQueues/999", 500)

	result := Create([]string{"100", "101", "102"}, false, false)

	if result["ok"] != false {
		t.Fatalf("result = %#v", result)
	}
	if result["error"] != "add to queue returned unexpected response" {
		t.Fatalf("error = %#v", result["error"])
	}
	if result["partialQueueID"] != "999" {
		t.Fatalf("partialQueueID = %#v", result["partialQueueID"])
	}
	if result["rollbackAttempted"] != true {
		t.Fatalf("rollbackAttempted = %#v", result["rollbackAttempted"])
	}
	if got := f.callCount("DELETE", "/playQueues/999"); got != 1 {
		t.Fatalf("rollback DELETE calls = %d, want 1", got)
	}
}

// --- Start (queue-start, B3) --------------------------------------------------

// appleTVOn is appleTV() with its Companion baseurl pointed at a live test
// server so PlayQueue's bind lands somewhere real.
func appleTVOn(baseurl string) jsonx.J {
	return jsonx.J{"machineIdentifier": "abc", "name": "Apple TV", "baseurl": baseurl}
}

func TestStartNoStateReturnsNoActiveQueue(t *testing.T) {
	newFakePMS(t) // no saved state

	result := Start(appleTV())

	want := jsonx.J{"ok": false, "error": "no active queue on Apple TV"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

func TestStartHappyPathBindsSavedQueue(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "555", "999")
	f.serverIDRoute(serverMID)
	f.onStatus("GET", "/player/playback/playMedia", 200)

	result := Start(appleTVOn(f.srv.URL))

	if result["ok"] != true {
		t.Fatalf("result = %#v, want ok:true", result)
	}
	if result["playQueueID"] != "555" || result["selectedItemID"] != "999" {
		t.Fatalf("result = %#v, want IDs echoed", result)
	}
	q := f.lastQuery("GET", "/player/playback/playMedia")
	if q.Get("key") != "/playQueues/555" {
		t.Fatalf("key = %q, want /playQueues/555", q.Get("key"))
	}
	if q.Get("playQueueID") != "555" || q.Get("playQueueSelectedItemID") != "999" {
		t.Fatalf("playMedia bound wrong queue: %v", q)
	}
}

func TestStartTransportFailurePreservesStateAndFlagsUnreachable(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "555", "999")
	f.serverIDRoute(serverMID)
	// Companion baseurl points at a dead port -> "connection failed:".
	result := Start(appleTVOn("http://127.0.0.1:1"))

	if result["ok"] != false {
		t.Fatalf("result = %#v, want ok:false", result)
	}
	if result["clientUnreachable"] != true {
		t.Fatalf("clientUnreachable = %#v, want true", result["clientUnreachable"])
	}
	if result["playQueueID"] != "555" || result["selectedItemID"] != "999" {
		t.Fatalf("result = %#v, want IDs echoed", result)
	}
	if queuestate.Load("abc") == nil {
		t.Fatalf("staged state must be kept for retry")
	}
}

// --- Add ----------------------------------------------------------------------

func TestAddUnexpectedResponseError(t *testing.T) {
	f := newFakePMS(t)
	f.serverIDRoute(serverMID)
	f.onJSON("PUT", "/playQueues/999", jsonx.J{"MediaContainer": jsonx.J{}})

	result := Add("999", "100")

	want := jsonx.J{"ok": false, "error": "add to queue returned unexpected response"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

func TestAddReturnsErrorWhenServerIDMissing(t *testing.T) {
	newFakePMS(t) // no "/" route -> GetServerMachineID() == ""

	result := Add("999", "100")

	want := jsonx.J{"ok": false, "error": "could not retrieve server machineIdentifier"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

// --- clientLabel ----------------------------------------------------------

func TestClientLabelPrefersName(t *testing.T) {
	got := clientLabel(jsonx.J{"name": "Apple TV", "machineIdentifier": "abc"})
	if got != "Apple TV" {
		t.Fatalf("clientLabel = %q", got)
	}
}

func TestClientLabelFallsBackToMachineIdentifier(t *testing.T) {
	got := clientLabel(jsonx.J{"machineIdentifier": "abc"})
	if got != "abc" {
		t.Fatalf("clientLabel = %q", got)
	}
}

func TestClientLabelFallsBackToClientWhenBothMissing(t *testing.T) {
	got := clientLabel(jsonx.J{})
	if got != "client" {
		t.Fatalf("clientLabel = %q", got)
	}
}

func TestClientLabelSkipsEmptyName(t *testing.T) {
	got := clientLabel(jsonx.J{"name": "", "machineIdentifier": "abc"})
	if got != "abc" {
		t.Fatalf("clientLabel = %q", got)
	}
}

// --- resolveQueueID ---------------------------------------------------------

func TestResolveQueueIDReturnsPersisted(t *testing.T) {
	testutil.Setup(t, "http://unused")
	queuestate.Save("abc", "5535", "42687")

	qid, err := resolveQueueID(appleTV())

	if err != nil {
		t.Fatalf("err = %#v", err)
	}
	if qid != "5535" {
		t.Fatalf("qid = %q", qid)
	}
}

func TestResolveQueueIDNoStateReturnsNoActiveQueue(t *testing.T) {
	testutil.Setup(t, "http://unused")

	qid, err := resolveQueueID(appleTV())

	if qid != "" {
		t.Fatalf("qid = %q, want empty", qid)
	}
	want := jsonx.J{"ok": false, "error": "no active queue on Apple TV"}
	if !reflect.DeepEqual(err, want) {
		t.Fatalf("err = %#v, want %#v", err, want)
	}
}

func TestResolveQueueIDDifferentClientDoesntLeak(t *testing.T) {
	testutil.Setup(t, "http://unused")
	queuestate.Save("xyz", "5535", "42687")

	qid, err := resolveQueueID(appleTV())

	if qid != "" {
		t.Fatalf("qid = %q, want empty", qid)
	}
	if err["error"] != "no active queue on Apple TV" {
		t.Fatalf("err = %#v", err)
	}
}

func TestResolveQueueIDUsesMachineIdentifierNotName(t *testing.T) {
	testutil.Setup(t, "http://unused")
	queuestate.Save("Apple TV", "5535", "42687") // saved against name, not mid

	qid, _ := resolveQueueID(appleTV()) // mid="abc" does not match

	if qid != "" {
		t.Fatalf("qid = %q, want empty", qid)
	}
}

func TestResolveQueueIDNoMachineIdentifierUsesNameInError(t *testing.T) {
	testutil.Setup(t, "http://unused")

	qid, err := resolveQueueID(jsonx.J{"name": "X"})

	if qid != "" {
		t.Fatalf("qid = %q, want empty", qid)
	}
	if err["error"] != "no active queue on X" {
		t.Fatalf("err = %#v", err)
	}
}

// --- Show: empty vs populated -------------------------------------------------

func TestShowReturnsEmptyWhenNoState(t *testing.T) {
	newFakePMS(t)

	result := Show(appleTV())

	want := jsonx.J{"ok": true, "state": "empty", "client": "Apple TV", "items": []jsonx.J{}}
	if !reflect.DeepEqual(normalize(t, result), normalize(t, want)) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

func TestShowReturnsEmptyWhenPMSReturnsEmptyMetadata(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "5535", "42687")
	f.onJSON("GET", "/playQueues/5535", jsonx.J{"MediaContainer": jsonx.J{"Metadata": []any{}}})

	result := Show(appleTV())

	if result["ok"] != true || result["state"] != "empty" {
		t.Fatalf("result = %#v", result)
	}
	items, _ := result["items"].([]jsonx.J)
	if len(items) != 0 {
		t.Fatalf("items = %#v", result["items"])
	}
}

// C4 (finding 7): a saved queue id that 404s (genuine prune OR transient) makes
// Show degrade to the same empty state as a never-created queue — but it must
// NOT delete the saved entry, so a transient 404 can't destroy an addressable
// queue. Self-heal is the next successful queue Save.
func TestShow404KeepsStateAndReturnsEmpty(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "5535", "42687")
	f.onStatus("GET", "/playQueues/5535", 404)

	result := Show(appleTV())

	want := jsonx.J{"ok": true, "state": "empty", "client": "Apple TV", "items": []jsonx.J{}}
	if !reflect.DeepEqual(normalize(t, result), normalize(t, want)) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	if queuestate.Load("abc") == nil {
		t.Fatalf("state must be KEPT on 404 (finding 7) — a transient 404 must not delete an addressable queue")
	}
}

// B4: a non-404 error on the queue read surfaces as an error shape (exit
// codes preserved via the message) and does NOT clear state.
func TestShowNon404ErrorReturnsErrorShapeAndKeepsState(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "5535", "42687")
	f.onStatus("GET", "/playQueues/5535", 500)

	result := Show(appleTV())

	if result["ok"] != false {
		t.Fatalf("result = %#v, want ok:false", result)
	}
	if errStr, _ := result["error"].(string); errStr == "" {
		t.Fatalf("error missing: %#v", result)
	}
	if queuestate.Load("abc") == nil {
		t.Fatalf("state must be kept on non-404 error")
	}
}

func TestShowPopulatedMatchesGoldenAndOmitsRatingKey(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "5732", "45136")
	f.onJSON("GET", "/playQueues/5732", jsonx.J{
		"MediaContainer": jsonx.J{
			"playQueueSelectedItemID": 45136,
			"Metadata": []any{
				jsonx.J{"playQueueItemID": 45136, "title": "The Birds", "type": "movie", "year": 2017, "duration": 6698208, "ratingKey": "999"},
				jsonx.J{"playQueueItemID": 45137, "title": "Casablanca", "type": "movie", "year": 1998, "duration": 5689824, "ratingKey": "998"},
			},
		},
	})

	result := Show(appleTV())

	golden := loadGolden(t, "queue-show.json")
	got := normalize(t, result)
	if !reflect.DeepEqual(got, golden) {
		t.Fatalf("result = %#v, want golden %#v", got, golden)
	}
	for _, raw := range got["items"].([]any) {
		item := raw.(map[string]any)
		if _, present := item["ratingKey"]; present {
			t.Fatalf("item carries ratingKey, must not: %#v", item)
		}
	}
}

// --- Shuffle / Unshuffle / RemoveItem: no-active-queue passthrough -----------

func TestShuffleReturnsNoActiveQueueErrorVerbatim(t *testing.T) {
	f := newFakePMS(t)

	result := Shuffle(appleTV())

	want := jsonx.J{"ok": false, "error": "no active queue on Apple TV"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	if len(f.calls) != 0 {
		t.Fatalf("expected no network calls, got %#v", f.calls)
	}
}

func TestUnshuffleReturnsNoActiveQueueErrorVerbatim(t *testing.T) {
	f := newFakePMS(t)

	result := Unshuffle(appleTV())

	want := jsonx.J{"ok": false, "error": "no active queue on Apple TV"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	if len(f.calls) != 0 {
		t.Fatalf("expected no network calls, got %#v", f.calls)
	}
}

func TestRemoveItemReturnsNoActiveQueueErrorVerbatim(t *testing.T) {
	f := newFakePMS(t)

	result := RemoveItem(appleTV(), "999")

	want := jsonx.J{"ok": false, "error": "no active queue on Apple TV"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	if len(f.calls) != 0 {
		t.Fatalf("expected no network calls, got %#v", f.calls)
	}
}

func TestShuffleAndUnshufflePutCorrectPaths(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "5535", "42687")
	f.onJSON("PUT", "/playQueues/5535/shuffle", jsonx.J{"MediaContainer": jsonx.J{}})
	f.onJSON("PUT", "/playQueues/5535/unshuffle", jsonx.J{"MediaContainer": jsonx.J{}})

	if r := Shuffle(appleTV()); r["ok"] != true {
		t.Fatalf("Shuffle result = %#v", r)
	}
	if r := Unshuffle(appleTV()); r["ok"] != true {
		t.Fatalf("Unshuffle result = %#v", r)
	}
	if f.callCount("PUT", "/playQueues/5535/shuffle") != 1 {
		t.Fatalf("shuffle PUT count = %d", f.callCount("PUT", "/playQueues/5535/shuffle"))
	}
	if f.callCount("PUT", "/playQueues/5535/unshuffle") != 1 {
		t.Fatalf("unshuffle PUT count = %d", f.callCount("PUT", "/playQueues/5535/unshuffle"))
	}
}

func TestRemoveItemDeletesCorrectPath(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "5535", "42687")
	f.onJSON("DELETE", "/playQueues/5535/items/321", jsonx.J{"MediaContainer": jsonx.J{}})

	result := RemoveItem(appleTV(), "321")

	if result["ok"] != true {
		t.Fatalf("result = %#v", result)
	}
	if f.callCount("DELETE", "/playQueues/5535/items/321") != 1 {
		t.Fatalf("DELETE count = %d", f.callCount("DELETE", "/playQueues/5535/items/321"))
	}
}

// --- Clear ---------------------------------------------------------------------

func TestClearRemovesPersistedState(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "5535", "42687")
	f.onJSON("DELETE", "/playQueues/5535/items", jsonx.J{"MediaContainer": jsonx.J{}})

	result := Clear(appleTV())

	if result["ok"] != true {
		t.Fatalf("result = %#v", result)
	}
	if queuestate.Load("abc") != nil {
		t.Fatalf("state not cleared: %#v", queuestate.Load("abc"))
	}
}

// B4: clearing an already-pruned queue is idempotent success — the DELETE
// 404s, we drop the stale entry and still report ok:true.
func TestClear404IsIdempotentSuccessAndClearsState(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "5535", "42687")
	f.onStatus("DELETE", "/playQueues/5535/items", 404)

	result := Clear(appleTV())

	if result["ok"] != true {
		t.Fatalf("result = %#v, want ok:true", result)
	}
	if queuestate.Load("abc") != nil {
		t.Fatalf("stale state not cleared: %#v", queuestate.Load("abc"))
	}
}

func TestClearReturnsNoActiveQueueWhenStateEmpty(t *testing.T) {
	newFakePMS(t)

	result := Clear(appleTV())

	if result["ok"] != false {
		t.Fatalf("result = %#v", result)
	}
	if result["error"] != "no active queue on Apple TV" {
		t.Fatalf("error = %#v", result["error"])
	}
}

// --- AddToClient ---------------------------------------------------------------

func TestAddToClientEmptyKeysRejected(t *testing.T) {
	f := newFakePMS(t)

	result := AddToClient(appleTV(), []string{})

	want := jsonx.J{"ok": false, "error": "add requires at least one ratingKey"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	if len(f.calls) != 0 {
		t.Fatalf("expected no network calls, got %#v", f.calls)
	}
}

func TestAddToClientNoActiveQueueReturnsResolveError(t *testing.T) {
	newFakePMS(t)

	result := AddToClient(appleTV(), []string{"100"})

	want := jsonx.J{"ok": false, "error": "no active queue on Apple TV"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

// C4 (finding 7): a saved id that 404s on the size-read makes AddToClient report
// no-active-queue rather than hard-exiting — but it must NOT delete the saved
// entry, so a transient 404 can't destroy an addressable queue.
func TestAddToClient404KeepsStateAndReturnsNoActiveQueue(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "5582", "1")
	f.serverIDRoute(serverMID)
	f.onStatus("GET", "/playQueues/5582", 404)

	result := AddToClient(appleTV(), []string{"100"})

	want := jsonx.J{"ok": false, "error": "no active queue on Apple TV"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	if queuestate.Load("abc") == nil {
		t.Fatalf("state must be KEPT on 404 (finding 7) — a transient 404 must not delete an addressable queue")
	}
}

func TestAddToClientSingleKeyAppendsOnce(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "5582", "1")
	f.serverIDRoute(serverMID)
	f.onSequence("GET", "/playQueues/5582", sizeResp(4), sizeResp(5))
	f.onJSON("PUT", "/playQueues/5582", jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "5582"}})

	result := AddToClient(appleTV(), []string{"100"})

	want := jsonx.J{"ok": true, "added": 1, "playQueueID": "5582"}
	if !reflect.DeepEqual(normalize(t, result), normalize(t, want)) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	puts := f.queriesFor("PUT", "/playQueues/5582")
	if len(puts) != 1 || puts[0].Get("uri") == "" {
		t.Fatalf("PUT calls = %#v", puts)
	}
}

// TestAddToClientVerifyGETTimeoutReturnsAddedInsteadOfExiting pins W6: the
// verify-GET after each PUT used to be print-and-exit api.Get, which would
// kill the process on a mid-loop timeout and lose the already-tracked
// `added` count. It's now api.TryGet, so a timeout surfaces as an
// ok:false envelope carrying `added` and `playQueueID` instead.
func TestAddToClientVerifyGETTimeoutReturnsAddedInsteadOfExiting(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "5582", "1")
	f.serverIDRoute(serverMID)
	f.onJSON("PUT", "/playQueues/5582", jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "5582"}})

	var calls int
	var mu sync.Mutex
	f.on("GET", "/playQueues/5582", func(r *http.Request) (int, any) {
		mu.Lock()
		n := calls
		calls++
		mu.Unlock()
		if n == 0 {
			// initial size check, before the loop's first PUT
			return 200, sizeResp(4)
		}
		// first per-key verify-GET hangs past the timeout
		time.Sleep(250 * time.Millisecond)
		return 200, nil
	})
	api.SetTimeoutOverride(0.05)
	t.Cleanup(api.ClearTimeoutOverride)

	result := AddToClient(appleTV(), []string{"100", "101"})

	if result["ok"] != false {
		t.Fatalf("result = %#v, want ok:false", result)
	}
	if result["added"] != 0 {
		t.Fatalf("added = %#v, want 0 (verify-GET failed before the first key could be confirmed)", result["added"])
	}
	if result["playQueueID"] != "5582" {
		t.Fatalf("playQueueID = %#v, want 5582", result["playQueueID"])
	}
	errStr, _ := result["error"].(string)
	if !strings.HasPrefix(errStr, "request timed out") {
		t.Fatalf("error = %q, want a \"request timed out\" prefix", errStr)
	}
}

func TestAddToClientMultiKeyLoopsPerKey(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "5582", "1")
	f.serverIDRoute(serverMID)
	f.onSequence("GET", "/playQueues/5582", sizeResp(4), sizeResp(5), sizeResp(6), sizeResp(7))
	f.onJSON("PUT", "/playQueues/5582", jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "5582"}})

	result := AddToClient(appleTV(), []string{"100", "101", "102"})

	want := jsonx.J{"ok": true, "added": 3, "playQueueID": "5582"}
	if !reflect.DeepEqual(normalize(t, result), normalize(t, want)) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	if got := f.callCount("PUT", "/playQueues/5582"); got != 3 {
		t.Fatalf("PUT count = %d, want 3", got)
	}
	if got := f.callCount("GET", "/playQueues/5582"); got != 4 {
		t.Fatalf("GET count = %d, want 4", got)
	}
}

func TestAddToClientPartialFailureFromUnderlyingAddReportsAdded(t *testing.T) {
	// Go can't monkeypatch add() the way the Python test does (it stubs
	// add() to return {"ok": False, "error": "HTTP 404: gone"} directly);
	// instead we drive the real "unexpected response" failure inside Add()
	// on the second key. The behavior under test -- add_to_client surfaces
	// `added` and `playQueueID` when a per-key add fails -- is preserved
	// even though the resulting error string differs from Python's mock.
	f := newFakePMS(t)
	queuestate.Save("abc", "5582", "1")
	f.serverIDRoute(serverMID)
	f.onSequence("GET", "/playQueues/5582", sizeResp(4), sizeResp(5))
	f.onSequence("PUT", "/playQueues/5582",
		jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "5582"}}, // key 100: ok
		jsonx.J{"MediaContainer": jsonx.J{}},                      // key 101: unexpected response
	)

	result := AddToClient(appleTV(), []string{"100", "101", "102"})

	if result["ok"] != false {
		t.Fatalf("result = %#v", result)
	}
	if result["added"] != 1 {
		t.Fatalf("added = %#v, want 1", result["added"])
	}
	if result["playQueueID"] != "5582" {
		t.Fatalf("playQueueID = %#v", result["playQueueID"])
	}
	if result["error"] != "add to queue returned unexpected response" {
		t.Fatalf("error = %#v", result["error"])
	}
}

func TestAddToClientDetectsPMSSilentNoOpOnBogusKey(t *testing.T) {
	f := newFakePMS(t)
	queuestate.Save("abc", "5582", "1")
	f.serverIDRoute(serverMID)
	f.onSequence("GET", "/playQueues/5582", sizeResp(4), sizeResp(4)) // no growth after add
	f.onJSON("PUT", "/playQueues/5582", jsonx.J{"MediaContainer": jsonx.J{"playQueueID": "5582"}})

	result := AddToClient(appleTV(), []string{"99999999"})

	want := jsonx.J{
		"ok":          false,
		"error":       "PMS accepted PUT but did not add ratingKey 99999999 — likely unknown or invalid",
		"added":       0,
		"playQueueID": "5582",
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}
