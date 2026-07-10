package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/testutil"
)

func TestFormatHTTPError(t *testing.T) {
	cases := []struct {
		name   string
		status int
		ctype  string
		body   string
		reason string
		want   string
	}{
		{"html title", 401, "text/html", "<html><head><title>Unauthorized\n  Access</title></head></html>", "Unauthorized", "HTTP 401: Unauthorized Access"},
		{"html no title", 500, "text/html", "<html><body><p>boom</p></body></html>", "Internal Server Error", "HTTP 500: boom"},
		{"json error field", 404, "application/json", `{"error": "not here"}`, "Not Found", "HTTP 404: not here"},
		{"json errors list dict", 400, "application/json", `{"errors": [{"message": "bad param"}]}`, "Bad Request", "HTTP 400: bad param"},
		{"json errors list string", 400, "application/json", `{"errors": ["oops"]}`, "Bad Request", "HTTP 400: oops"},
		{"plain text", 503, "text/plain", "server melting", "Service Unavailable", "HTTP 503: server melting"},
		{"empty body falls to reason", 502, "", "", "Bad Gateway", "HTTP 502: Bad Gateway"},
		{"sniffed html without ctype", 403, "", "  <!DOCTYPE html><html><title>Forbidden</title></html>", "Forbidden", "HTTP 403: Forbidden"},
		{"json fallback truncation", 422, "application/json", `{"weird": true}`, "Unprocessable Entity", `HTTP 422: {"weird": true}`},
	}
	for _, c := range cases {
		if got := api.FormatHTTPError(c.status, c.ctype, c.body, c.reason); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRequestHappyPathAndHeaders(t *testing.T) {
	var gotHeaders http.Header
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotURL = r.URL.String()
		w.Write([]byte(`{"MediaContainer": {"size": 1}}`))
	}))
	defer srv.Close()
	testutil.Setup(t, srv.URL)

	v := api.Get("/library/sections", url.Values{"q": {"x"}})
	if jsonx.Num(jsonx.GetMap(v, "MediaContainer")["size"]) != 1 {
		t.Fatalf("bad parse: %#v", v)
	}
	// Contract item 1: controller header + stable client identifier on every
	// PMS request.
	if gotHeaders.Get("X-Plex-Provides") != "controller" {
		t.Fatal("X-Plex-Provides: controller missing")
	}
	if gotHeaders.Get("X-Plex-Token") != "test-token" {
		t.Fatal("token header missing")
	}
	if gotHeaders.Get("X-Plex-Client-Identifier") != "plexctl-test" {
		t.Fatal("client identifier missing")
	}
	if !strings.Contains(gotURL, "q=x") {
		t.Fatalf("params not sent: %s", gotURL)
	}
}

func TestRequestEmptyBodyIsEmptyMap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200) // PMS PUT /library/parts returns an empty 200 body
	}))
	defer srv.Close()
	testutil.Setup(t, srv.URL)
	if v := api.Put("/library/parts/1", nil); len(v) != 0 {
		t.Fatalf("want empty map, got %#v", v)
	}
}

func TestHTTPErrorExitsOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", 404)
	}))
	defer srv.Close()
	testutil.Setup(t, srv.URL)
	out, code := testutil.Capture(t, func() { api.Get("/nope", nil) })
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, `"error":"HTTP 404: gone"`) {
		t.Fatalf("error shape drifted: %q", out)
	}
}

func TestTimeoutClassifiesAndExitsTwo(t *testing.T) {
	// Contract item 10: read timeouts are kind=timeout, message prefix
	// "request timed out:", exit 2.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()
	testutil.Setup(t, srv.URL)
	api.SetTimeoutOverride(0.05)
	t.Cleanup(func() { api.ClearTimeoutOverride() })

	out, code := testutil.Capture(t, func() { api.Get("/slow", nil) })
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(out, `"error":"request timed out:`) {
		t.Fatalf("timeout prefix drifted: %q", out)
	}
}

func TestConnectionRefusedClassifies(t *testing.T) {
	testutil.Setup(t, "http://127.0.0.1:1") // nothing listens on port 1
	api.SetTimeoutOverride(2)
	t.Cleanup(func() { api.ClearTimeoutOverride() })
	_, err := api.TryGet("/x", nil)
	if err == nil {
		t.Fatal("want error")
	}
	apiErr, ok := err.(*api.Error)
	if !ok {
		t.Fatalf("want *api.Error, got %T", err)
	}
	if apiErr.Kind != "error" || !strings.HasPrefix(apiErr.Message, "connection failed:") {
		t.Fatalf("classification drifted: kind=%s msg=%q", apiErr.Kind, apiErr.Message)
	}
}

func TestInvalidJSONClassifies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<xml>not json</xml>"))
	}))
	defer srv.Close()
	testutil.Setup(t, srv.URL)
	_, err := api.TryGet("/x", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "invalid JSON response:") {
		t.Fatalf("want invalid JSON classification, got %v", err)
	}
}

func TestPlexTVGetReturnsListAndPrefix(t *testing.T) {
	// devices.json is a JSON array; error prefix is "plex.tv ".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/devices.json" {
			w.Write([]byte(`[{"name": "Apple TV"}]`))
			return
		}
		http.Error(w, "nope", 500)
	}))
	defer srv.Close()
	testutil.Setup(t, srv.URL)
	// PlexTVGet is hardwired to plex.tv; exercise via ExitOnError with the
	// test base instead.
	v := api.ExitOnError("GET", srv.URL, "/devices.json", nil, "plex.tv ")
	list, ok := v.([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("want 1-element list, got %#v", v)
	}
	out, code := testutil.Capture(t, func() {
		api.ExitOnError("GET", srv.URL, "/boom", nil, "plex.tv ")
	})
	if code != 1 || !strings.Contains(out, `"error":"plex.tv HTTP 500:`) {
		t.Fatalf("prefix drifted: code=%d out=%q", code, out)
	}
}

func TestBuildURLPathWithEmbeddedQuery(t *testing.T) {
	// Smart-collection content paths already carry a query string.
	got := api.BuildURL("http://x:32400/", "/library/sections/1/all?type=1", url.Values{"a": {"b"}})
	if got != "http://x:32400/library/sections/1/all?type=1&a=b" {
		t.Fatalf("BuildURL = %q", got)
	}
}

func TestDefaultTimeoutResolution(t *testing.T) {
	dir := testutil.Setup(t, "http://unused")
	_ = dir
	t.Setenv("PLEXCTL_TIMEOUT", "3.5")
	if got := api.DefaultTimeout(); got != 3.5 {
		t.Fatalf("env timeout = %v, want 3.5", got)
	}
	api.SetTimeoutOverride(1.25)
	t.Cleanup(func() { api.ClearTimeoutOverride() })
	if got := api.DefaultTimeout(); got != 1.25 {
		t.Fatalf("override should win, got %v", got)
	}
}

// TestDefaultTimeoutClampsNonPositive pins W1: a non-positive or
// unparseable value from any source is never returned as-is — it would
// make http.Client.Timeout 0, which is Go for no timeout at all — so it
// falls through exactly like an absent/unparseable source would.
func TestDefaultTimeoutClampsNonPositive(t *testing.T) {
	testutil.Setup(t, "http://unused")

	t.Run("env zero falls through to default", func(t *testing.T) {
		t.Setenv("PLEXCTL_TIMEOUT", "0")
		if got := api.DefaultTimeout(); got != api.DefaultTimeoutSeconds {
			t.Fatalf("PLEXCTL_TIMEOUT=0 resolved to %v, want %v", got, api.DefaultTimeoutSeconds)
		}
	})

	t.Run("env unparseable falls through to default", func(t *testing.T) {
		t.Setenv("PLEXCTL_TIMEOUT", "abc")
		if got := api.DefaultTimeout(); got != api.DefaultTimeoutSeconds {
			t.Fatalf("PLEXCTL_TIMEOUT=abc resolved to %v, want %v", got, api.DefaultTimeoutSeconds)
		}
	})

	t.Run("override zero falls through to default (defensive resolver clamp)", func(t *testing.T) {
		api.SetTimeoutOverride(0)
		t.Cleanup(func() { api.ClearTimeoutOverride() })
		if got := api.DefaultTimeout(); got != api.DefaultTimeoutSeconds {
			t.Fatalf("override=0 resolved to %v, want %v", got, api.DefaultTimeoutSeconds)
		}
	})

	t.Run("override negative falls through to default", func(t *testing.T) {
		api.SetTimeoutOverride(-1)
		t.Cleanup(func() { api.ClearTimeoutOverride() })
		if got := api.DefaultTimeout(); got != api.DefaultTimeoutSeconds {
			t.Fatalf("override=-1 resolved to %v, want %v", got, api.DefaultTimeoutSeconds)
		}
	})
}
