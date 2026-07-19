package api_test

import (
	"errors"
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

func TestHTTP404CodesNotFound(t *testing.T) {
	// v2 (docs/error_model_v2.md): PMS 404 → PLEX_NOT_FOUND, exit 2,
	// structured envelope with http_status.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", 404)
	}))
	defer srv.Close()
	testutil.Setup(t, srv.URL)
	out, code := testutil.Capture(t, func() { api.Get("/nope", nil) })
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(out, `"code":"PLEX_NOT_FOUND"`) || !strings.Contains(out, `"http_status":404`) {
		t.Fatalf("error shape drifted: %q", out)
	}
	if !strings.Contains(out, `"message":"HTTP 404: gone"`) {
		t.Fatalf("message drifted: %q", out)
	}
}

func TestTimeoutClassifiesAndExitsThree(t *testing.T) {
	// v2: read timeouts → TRANSPORT_TIMEOUT, exit 3 (batch callers retry on
	// the code, not the exit).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()
	testutil.Setup(t, srv.URL)
	api.SetTimeoutOverride(0.05)
	t.Cleanup(func() { api.ClearTimeoutOverride() })

	out, code := testutil.Capture(t, func() { api.Get("/slow", nil) })
	if code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
	if !strings.Contains(out, `"code":"TRANSPORT_TIMEOUT"`) || !strings.Contains(out, `"message":"request timed out:`) {
		t.Fatalf("timeout classification drifted: %q", out)
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
	// v2: upstream 5xx → PLEX_SERVER_ERROR, exit 2; the "plex.tv " prefix
	// survives in the human-readable message only.
	if code != 2 || !strings.Contains(out, `"code":"PLEX_SERVER_ERROR"`) || !strings.Contains(out, `"message":"plex.tv HTTP 500:`) {
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

// TestRequestRefusesRedirect pins W1 (finding 1): a PMS that 302s must never
// cause the token to be forwarded to the redirect target, and the resulting
// error must classify as connection-failed with no query string leaked.
func TestRequestRefusesRedirect(t *testing.T) {
	var targetHit bool
	var targetGotToken bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit = true
		if r.Header.Get("X-Plex-Token") != "" {
			targetGotToken = true
		}
		w.Write([]byte(`{}`))
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	testutil.Setup(t, srv.URL)

	_, err := api.TryGet("/x", url.Values{"q": {"SECRETPHRASE"}})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if targetHit {
		t.Fatal("redirect target received a request — CheckRedirect did not fire before the request")
	}
	if targetGotToken {
		t.Fatal("X-Plex-Token reached the redirect target")
	}
	apiErr, ok := err.(*api.Error)
	if !ok {
		t.Fatalf("want *api.Error, got %T", err)
	}
	if !strings.HasPrefix(apiErr.Message, "connection failed:") {
		t.Fatalf("want 'connection failed:' prefix, got %q", apiErr.Message)
	}
	if !strings.Contains(apiErr.Message, "redirect refused") {
		t.Fatalf("want 'redirect refused' in message, got %q", apiErr.Message)
	}
	if strings.Contains(apiErr.Message, "SECRETPHRASE") || strings.Contains(apiErr.Message, "?") {
		t.Fatalf("query string leaked into error: %q", apiErr.Message)
	}
}

// TestSanitizeError table-tests the *url.Error rendering directly, including
// a nested *url.Error (a redirect refusal wrapped again by the outer Do
// call) — the recursive case must not bypass sanitization at either layer.
func TestSanitizeError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "strips query string",
			err:  &url.Error{Op: "Get", URL: "http://host.example:32400/library?token=SECRET", Err: errors.New("boom")},
			want: `Get "http://host.example:32400/library": boom`,
		},
		{
			name: "strips userinfo",
			err:  &url.Error{Op: "Get", URL: "http://user:pass@host.example/path", Err: errors.New("boom")},
			want: `Get "http://host.example/path": boom`,
		},
		{
			name: "strips fragment",
			err:  &url.Error{Op: "Get", URL: "http://host.example/path#secret-fragment", Err: errors.New("boom")},
			want: `Get "http://host.example/path": boom`,
		},
		{
			name: "recurses through a nested url.Error",
			err: &url.Error{Op: "Get", URL: "http://pms.example:32400/library?token=SECRET", Err: &url.Error{
				Op: "dial", URL: "http://pms.example:32400/?token=SECRET", Err: errors.New("dial tcp 10.0.0.5:32400: connect: connection refused"),
			}},
			want: `Get "http://pms.example:32400/library": dial "http://pms.example:32400/": dial tcp 10.0.0.5:32400: connect: connection refused`,
		},
		{
			name: "unparseable URL drops it entirely, still recurses",
			err:  &url.Error{Op: "Get", URL: "http://host.example:badport/secret?token=X", Err: errors.New("boom")},
			want: "Get: boom",
		},
		{
			name: "non-url.Error passes through unchanged",
			err:  errors.New("plain failure"),
			want: "plain failure",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := api.SanitizeError(c.err); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestSanitizeErrorHidesQueryOnRealTransportFailures exercises the sanitizer
// end-to-end through both classification branches with a live query string
// carrying a distinctive value that must never surface in the error.
func TestSanitizeErrorHidesQueryOnRealTransportFailures(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		testutil.Setup(t, "http://127.0.0.1:1") // nothing listens on port 1
		api.SetTimeoutOverride(2)
		t.Cleanup(func() { api.ClearTimeoutOverride() })
		_, err := api.TryGet("/x", url.Values{"query": {"SECRETPHRASE"}})
		if err == nil {
			t.Fatal("want error")
		}
		if strings.Contains(err.Error(), "SECRETPHRASE") {
			t.Fatalf("query leaked: %q", err.Error())
		}
		if !strings.Contains(err.Error(), "127.0.0.1:1") || !strings.Contains(err.Error(), "/x") {
			t.Fatalf("host/path should survive: %q", err.Error())
		}
	})

	t.Run("timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(2 * time.Second)
		}))
		defer srv.Close()
		testutil.Setup(t, srv.URL)
		api.SetTimeoutOverride(0.05)
		t.Cleanup(func() { api.ClearTimeoutOverride() })
		_, err := api.TryGet("/slow", url.Values{"query": {"SECRETPHRASE"}})
		if err == nil {
			t.Fatal("want error")
		}
		if strings.Contains(err.Error(), "SECRETPHRASE") {
			t.Fatalf("query leaked: %q", err.Error())
		}
		if !strings.Contains(err.Error(), "/slow") {
			t.Fatalf("path should survive: %q", err.Error())
		}
	})
}

// TestFormatHTTPErrorStripsControlChars pins W2 (finding 4): a remote body
// is untrusted input; control characters must never reach a terminal or log
// verbatim. \n and \t become a space; everything else below 0x20, plus DEL,
// is dropped.
func TestFormatHTTPErrorStripsControlChars(t *testing.T) {
	body := "line1\nline2\ttabbed\x01\x7Fend"
	got := api.FormatHTTPError(502, "", body, "Bad Gateway")
	want := "HTTP 502: line1 line2 tabbedend"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
