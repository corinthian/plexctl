package api_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/output"
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
	api.SetTimeoutForTest(50 * time.Millisecond)
	t.Cleanup(func() { api.ClearTimeoutForTest() })

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
	api.SetTimeoutForTest(2 * time.Second)
	t.Cleanup(func() { api.ClearTimeoutForTest() })
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

// TestInvalidJSONClassifies: the message is unchanged, but the code and exit
// move from TRANSPORT_FAILED 3 to DECODE_ERROR 4 (contract Part 3, plexctl
// decode rows). A malformed body is not a transport failure and never was.
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
	cli := api.Classify(api.AsError(err), api.TargetPMS)
	if cli.Code != output.CodeDecodeError || cli.ExitCode() != 4 {
		t.Fatalf("code = %q exit %d, want DECODE_ERROR exit 4", cli.Code, cli.ExitCode())
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

// TestDefaultTimeoutResolution was an assertion that PLEXCTL_TIMEOUT=3.5
// resolves to 3.5 seconds. Under the integer-seconds grammar (contract 2.1)
// a float from any source is an error, so the float case inverts and the
// ordering it was really testing keeps its own integer case.
func TestDefaultTimeoutResolution(t *testing.T) {
	testutil.Setup(t, "http://unused")

	t.Run("a float from the environment is rejected", func(t *testing.T) {
		t.Setenv("PLEXCTL_TIMEOUT", "3.5")
		if _, err := api.ResolveTimeout(false, ""); err == nil {
			t.Fatal("PLEXCTL_TIMEOUT=3.5 resolved without error")
		}
	})

	t.Run("the environment is used when the flag is unset", func(t *testing.T) {
		t.Setenv("PLEXCTL_TIMEOUT", "3")
		got, err := api.ResolveTimeout(false, "")
		if err != nil || got != 3*time.Second {
			t.Fatalf("env timeout = %v, %v; want 3s", got, err)
		}
	})

	t.Run("the flag outranks the environment", func(t *testing.T) {
		t.Setenv("PLEXCTL_TIMEOUT", "3")
		got, err := api.ResolveTimeout(true, "1")
		if err != nil || got != time.Second {
			t.Fatalf("flag timeout = %v, %v; want 1s", got, err)
		}
	})

	t.Run("nothing set resolves to the default", func(t *testing.T) {
		t.Setenv("PLEXCTL_TIMEOUT", "")
		got, err := api.ResolveTimeout(false, "")
		if err != nil || got != api.DefaultTimeout {
			t.Fatalf("default timeout = %v, %v; want %v", got, err, api.DefaultTimeout)
		}
	})
}

// TestNonPositiveAndMalformedTimeoutsAreRejected inverts what was
// TestDefaultTimeoutClampsNonPositive. The silent fall-through to the
// default is exactly what contract 2.1 abolishes: no source ever rescues an
// invalid higher-priority one, and nothing is skipped quietly.
func TestNonPositiveAndMalformedTimeoutsAreRejected(t *testing.T) {
	testutil.Setup(t, "http://unused")

	for _, raw := range []string{"0", "abc", "-1"} {
		t.Run("env "+raw, func(t *testing.T) {
			t.Setenv("PLEXCTL_TIMEOUT", raw)
			_, err := api.ResolveTimeout(false, "")
			if err == nil {
				t.Fatalf("PLEXCTL_TIMEOUT=%s resolved without error", raw)
			}
			if !strings.Contains(err.Error(), "$PLEXCTL_TIMEOUT") {
				t.Fatalf("error does not name the source: %v", err)
			}
		})
	}
}

// TestEnvTimeoutInvalidIsBadRequest covers the environment surface of
// contract 2.1's edge-case table, including the one value that is not an
// error: an empty variable counts as unset and the next candidate is
// consulted.
func TestEnvTimeoutInvalidIsBadRequest(t *testing.T) {
	testutil.Setup(t, "http://unused")

	for _, raw := range []string{"abc", "0", "-1", "10.5", "90000", "30s", " 30 ", "+30", "NaN", "Inf"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("PLEXCTL_TIMEOUT", raw)
			_, err := api.ResolveTimeout(false, "")
			if err == nil {
				t.Fatalf("PLEXCTL_TIMEOUT=%q resolved without error", raw)
			}
			if !strings.Contains(err.Error(), "$PLEXCTL_TIMEOUT") {
				t.Fatalf("error does not name $PLEXCTL_TIMEOUT: %v", err)
			}
		})
	}

	t.Run("empty is unset, not an error", func(t *testing.T) {
		t.Setenv("PLEXCTL_TIMEOUT", "")
		got, err := api.ResolveTimeout(false, "")
		if err != nil {
			t.Fatalf("empty PLEXCTL_TIMEOUT errored: %v", err)
		}
		if got != api.DefaultTimeout {
			t.Fatalf("empty PLEXCTL_TIMEOUT resolved to %v, want the default %v", got, api.DefaultTimeout)
		}
	})
}

// writeConfigTimeout rewrites the test config with a raw TOML timeout line,
// so each case exercises the real type the TOML decoder produces.
func writeConfigTimeout(t *testing.T, dir, line string) {
	t.Helper()
	body := "server_url = \"http://unused\"\ntoken = \"test-token\"\n" + line + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestConfigTimeoutTypeRules pins contract 2.1's TOML rows: an integer is the
// only accepted form in a config file, and every other type is an error
// naming the file, never a silent fall-through to the default.
func TestConfigTimeoutTypeRules(t *testing.T) {
	t.Setenv("PLEXCTL_TIMEOUT", "")

	t.Run("integer is accepted", func(t *testing.T) {
		dir := testutil.Setup(t, "http://unused")
		writeConfigTimeout(t, dir, "timeout = 10")
		got, err := api.ResolveTimeout(false, "")
		if err != nil || got != 10*time.Second {
			t.Fatalf("config timeout = %v, %v; want 10s", got, err)
		}
	})

	for _, line := range []string{
		"timeout = 10.5",
		"timeout = 10.0",
		"timeout = \"10\"",
		"timeout = \"10s\"",
		"timeout = 0",
		"timeout = -1",
		"timeout = 90000",
		"timeout = true",
		"timeout = [10]",
	} {
		t.Run(line, func(t *testing.T) {
			dir := testutil.Setup(t, "http://unused")
			writeConfigTimeout(t, dir, line)
			_, err := api.ResolveTimeout(false, "")
			if err == nil {
				t.Fatalf("%s resolved without error", line)
			}
			if !strings.Contains(err.Error(), "config timeout (") {
				t.Fatalf("error does not name the config source: %v", err)
			}
		})
	}

	t.Run("absent is unset, not an error", func(t *testing.T) {
		testutil.Setup(t, "http://unused")
		got, err := api.ResolveTimeout(false, "")
		if err != nil || got != api.DefaultTimeout {
			t.Fatalf("absent config timeout = %v, %v; want the default", got, err)
		}
	})
}

// TestInvalidFlagBeatsValidConfig pins the no-rescue rule: the highest
// present source is authoritative even when it is wrong, and a valid lower
// source never repairs it.
func TestInvalidFlagBeatsValidConfig(t *testing.T) {
	dir := testutil.Setup(t, "http://unused")
	writeConfigTimeout(t, dir, "timeout = 30")
	t.Setenv("PLEXCTL_TIMEOUT", "")
	got, err := api.ResolveTimeout(true, "abc")
	if err == nil {
		t.Fatalf("--timeout abc resolved to %v instead of failing", got)
	}
	if !strings.Contains(err.Error(), "--timeout") {
		t.Fatalf("error does not name --timeout: %v", err)
	}
}

// TestInvalidEnvBeatsValidConfig is the same rule one rung down.
func TestInvalidEnvBeatsValidConfig(t *testing.T) {
	dir := testutil.Setup(t, "http://unused")
	writeConfigTimeout(t, dir, "timeout = 30")
	t.Setenv("PLEXCTL_TIMEOUT", "abc")
	if _, err := api.ResolveTimeout(false, ""); err == nil {
		t.Fatal("an invalid $PLEXCTL_TIMEOUT was rescued by the config file")
	}
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
	// xhttp's refusal reads "redirect to <scheme>://<host> refused:
	// redirects are not followed" where the inline CheckRedirect read
	// "redirect refused: destination <scheme>://<host><path>". Code and exit
	// do not change; only the wording does.
	if !strings.Contains(apiErr.Message, "refused: redirects are not followed") {
		t.Fatalf("want the refusal wording in message, got %q", apiErr.Message)
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
		api.SetTimeoutForTest(2 * time.Second)
		t.Cleanup(func() { api.ClearTimeoutForTest() })
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
		api.SetTimeoutForTest(50 * time.Millisecond)
		t.Cleanup(func() { api.ClearTimeoutForTest() })
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
