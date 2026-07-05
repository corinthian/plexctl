package commands_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/corinthian/plexctl/internal/testutil"
)

// --- fake PMS + plex.tv + Companion harness ----------------------------------
//
// One httptest server plays all three roles a command can talk to: PMS
// (config server_url points here directly), plex.tv (api.PlexTVGet is
// hardwired to the literal https://plex.tv host with no override seam, so
// http.DefaultTransport is patched to redirect that host here instead — this
// never touches internal/api), and a Companion client (the "Apple TV" active
// client's baseurl is set to this same server, since a plain HTTP GET doesn't
// care which role it's playing).

type capturedReq struct {
	method string
	path   string
}

type fakePMS struct {
	mu     sync.Mutex
	calls  []capturedReq
	routes map[string]map[string]func(r *http.Request) (int, any)
	srv    *httptest.Server
	dir    string
}

func newFakePMS(t *testing.T) *fakePMS {
	t.Helper()
	f := &fakePMS{routes: map[string]map[string]func(r *http.Request) (int, any){}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, capturedReq{method: r.Method, path: r.URL.Path})
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
	t.Cleanup(f.srv.Close)
	f.dir = testutil.Setup(t, f.srv.URL)
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

func (f *fakePMS) countMethod(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.method == method {
			n++
		}
	}
	return n
}

// hostRedirectTransport rewrites requests to a fixed host so they land on a
// local test server instead of the real internet.
type hostRedirectTransport struct {
	underlying http.RoundTripper
	host       string
	target     *url.URL
}

func (t *hostRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == t.host {
		req = req.Clone(req.Context())
		req.URL.Scheme = t.target.Scheme
		req.URL.Host = t.target.Host
	}
	return t.underlying.RoundTrip(req)
}

// redirectPlexTV patches http.DefaultTransport (restored via t.Cleanup) so
// api.PlexTVGet's hardcoded https://plex.tv calls land on f instead of the
// real network. Tests in this package never run in parallel, so the global
// swap is safe.
func (f *fakePMS) redirectPlexTV(t *testing.T) {
	t.Helper()
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	http.DefaultTransport = &hostRedirectTransport{underlying: old, host: "plex.tv", target: u}
	t.Cleanup(func() { http.DefaultTransport = old })
}

// resolvableClient wires /clients (PMS) + /devices.json (plex.tv, redirected)
// so clients.Resolve("") resolves the config's default_client ("Apple TV",
// per testutil.Setup) to an active client whose baseurl is f itself —
// letting Companion calls (playback.*) land on the same fake server.
func (f *fakePMS) resolvableClient(t *testing.T) {
	t.Helper()
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	f.onJSON("GET", "/clients", map[string]any{
		"MediaContainer": map[string]any{
			"Server": []any{
				map[string]any{
					"name":              "Apple TV",
					"machineIdentifier": "mid-appletv",
					"host":              u.Hostname(),
					"port":              u.Port(),
				},
			},
		},
	})
	f.redirectPlexTV(t)
	f.onJSON("GET", "/devices.json", []any{
		map[string]any{
			"name":       "Apple TV",
			"product":    "Plex for Apple TV",
			"version":    "1.0",
			"lastSeenAt": "0",
		},
	})
}

// --- small assertion helpers --------------------------------------------------

func trimNL(s string) string {
	return strings.TrimRight(s, "\n")
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

func mustUnmarshal(t *testing.T, s string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad json %q: %v", s, err)
	}
	return v
}
