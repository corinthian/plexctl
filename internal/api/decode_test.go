package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/testutil"
)

func serveRaw(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	testutil.Setup(t, srv.URL)
}

// TestValidPrefixPlusGarbageIsDecodeError pins contract Part 1 row E5: the
// single Decode plexctl used accepted a valid prefix and returned ok:true at
// exit 0, silently discarding whatever followed.
func TestValidPrefixPlusGarbageIsDecodeError(t *testing.T) {
	serveRaw(t, `{"a":1} junk`)

	out, code := testutil.Capture(t, func() { api.Get("/x", nil) })
	if code != 4 || !strings.Contains(out, `"code":"DECODE_ERROR"`) {
		t.Fatalf("exit = %d, want 4 with DECODE_ERROR; out=%s", code, out)
	}
}

// TestTwoJSONValuesIsDecodeError: NDJSON arriving on a non-NDJSON endpoint
// is a malformed response, not a stream.
func TestTwoJSONValuesIsDecodeError(t *testing.T) {
	serveRaw(t, `{"a":1}{"b":2}`)

	out, code := testutil.Capture(t, func() { api.Get("/x", nil) })
	if code != 4 || !strings.Contains(out, `"code":"DECODE_ERROR"`) {
		t.Fatalf("exit = %d, want 4 with DECODE_ERROR; out=%s", code, out)
	}
}

// TestTrailingWhitespaceIsFine: only non-whitespace after the value is an
// error.
func TestTrailingWhitespaceIsFine(t *testing.T) {
	serveRaw(t, "{\"a\":1}\n\n")

	v, err := api.TryGet("/x", nil)
	if err != nil {
		t.Fatalf("trailing whitespace rejected: %v", err)
	}
	if _, ok := v["a"]; !ok {
		t.Fatalf("body did not decode: %#v", v)
	}
}

// TestTruncatedBodyIsDecodeError moves this case from TRANSPORT_FAILED 3.
func TestTruncatedBodyIsDecodeError(t *testing.T) {
	serveRaw(t, `{"a":1`)

	out, code := testutil.Capture(t, func() { api.Get("/x", nil) })
	if code != 4 || !strings.Contains(out, `"code":"DECODE_ERROR"`) {
		t.Fatalf("exit = %d, want 4 with DECODE_ERROR; out=%s", code, out)
	}
}

// TestDecodeErrorForEveryTarget is the point of the item: a decode failure
// against plex.tv used to be reported as "retry shortly" and one from a
// client as "wake the device", advice that cannot help. Every target now
// gives DECODE_ERROR at exit 4 with no hint.
func TestDecodeErrorForEveryTarget(t *testing.T) {
	for name, target := range map[string]api.Target{
		"pms":    api.TargetPMS,
		"cloud":  api.TargetCloud,
		"client": api.TargetClient,
	} {
		t.Run(name, func(t *testing.T) {
			serveRaw(t, `<xml>not json</xml>`)
			_, err := api.TryGet("/x", nil)
			if err == nil {
				t.Fatal("want an error")
			}
			cli := api.Classify(api.AsError(err), target)
			if cli.Code != output.CodeDecodeError || cli.ExitCode() != 4 {
				t.Fatalf("code = %q exit %d, want DECODE_ERROR exit 4", cli.Code, cli.ExitCode())
			}
			if cli.Hint != "" {
				t.Fatalf("DECODE_ERROR must carry no hint, got %q", cli.Hint)
			}
		})
	}
}

// TestUseNumberSurvives pins that the migration to DecodeOne keeps what the
// hand-rolled decoder had: number literals are not re-rendered through
// float64.
func TestUseNumberSurvives(t *testing.T) {
	serveRaw(t, `{"big":9007199254740993,"float":9.0}`)

	v, err := api.TryGet("/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	big, ok := v["big"].(json.Number)
	if !ok {
		t.Fatalf("big is %T, want json.Number", v["big"])
	}
	if big.String() != "9007199254740993" {
		t.Fatalf("big = %s, want 9007199254740993", big.String())
	}
	f, ok := v["float"].(json.Number)
	if !ok {
		t.Fatalf("float is %T, want json.Number", v["float"])
	}
	if f.String() != "9.0" {
		t.Fatalf("float = %s, want 9.0", f.String())
	}
}

// TestEmptyAndWhitespaceBodiesStayEmptyMaps: contract 2.3's empty-body rows.
// The empty branch sits ahead of the decode and must stay there.
func TestEmptyAndWhitespaceBodiesStayEmptyMaps(t *testing.T) {
	for _, body := range []string{"", "   \n\t "} {
		t.Run(strings.TrimSpace(body)+"|", func(t *testing.T) {
			serveRaw(t, body)
			v, err := api.TryGet("/x", nil)
			if err != nil {
				t.Fatalf("empty body errored: %v", err)
			}
			if len(v) != 0 {
				t.Fatalf("want an empty map, got %#v", v)
			}
		})
	}
}

// TestNonJSONOn500IsNotADecodeError pins the row contract 2.3 forbids
// outright: an HTML error page from a proxy keeps the HTTP status's own
// code, and the body is used for the message only.
func TestNonJSONOn500IsNotADecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(500)
		w.Write([]byte("<html><title>Bad Gateway</title></html>"))
	}))
	defer srv.Close()
	testutil.Setup(t, srv.URL)

	out, code := testutil.Capture(t, func() { api.Get("/x", nil) })
	if code != 2 || !strings.Contains(out, `"code":"PLEX_SERVER_ERROR"`) {
		t.Fatalf("exit = %d, want 2 with PLEX_SERVER_ERROR; out=%s", code, out)
	}
}
