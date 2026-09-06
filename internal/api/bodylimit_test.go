package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/testutil"
)

// writeJSONOfSize writes a JSON object whose encoding is exactly n bytes:
// {"pad":"<n-10 spaces>"} — 10 characters of structure around the padding.
func writeJSONOfSize(w http.ResponseWriter, n int64) {
	const overhead = 10
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"pad":"`))
	const chunk = 1 << 20
	buf := make([]byte, chunk)
	for i := range buf {
		buf[i] = ' '
	}
	remaining := n - overhead
	for remaining > 0 {
		size := int64(chunk)
		if remaining < size {
			size = remaining
		}
		w.Write(buf[:size])
		remaining -= size
	}
	w.Write([]byte(`"}`))
}

func serveBody(t *testing.T, status int, n int64) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 200 {
			w.WriteHeader(status)
		}
		writeJSONOfSize(w, n)
	}))
	t.Cleanup(srv.Close)
	testutil.Setup(t, srv.URL)
}

// TestOversizeBodyIsDecodeError pins contract 2.3: a body over the bound is
// never truncated and never decoded. It was silently truncated at 32 MiB
// before, which surfaced downstream as a JSON parse error — a lie about what
// went wrong.
func TestOversizeBodyIsDecodeError(t *testing.T) {
	serveBody(t, 200, api.BodyLimit+1)

	out, code := testutil.Capture(t, func() { api.Get("/library/sections/1/all", nil) })
	if code != 4 {
		t.Fatalf("exit = %d, want 4; out=%s", code, out)
	}
	if !strings.Contains(out, `"code":"DECODE_ERROR"`) {
		t.Fatalf("want DECODE_ERROR, got %s", out)
	}
	for _, want := range []string{"64 MiB", "GET", "/library/sections/1/all"} {
		if !strings.Contains(out, want) {
			t.Errorf("message does not name %q: %s", want, out)
		}
	}
	if strings.Contains(out, `"hint"`) {
		t.Errorf("DECODE_ERROR must carry no hint: %s", out)
	}
}

// TestExactLimitBodySucceeds pins the boundary: exactly the limit is a valid
// body, limit+1 is oversize.
func TestExactLimitBodySucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a 64 MiB body")
	}
	serveBody(t, 200, api.BodyLimit)

	v, err := api.TryGet("/x", nil)
	if err != nil {
		t.Fatalf("a body of exactly BodyLimit bytes failed: %v", err)
	}
	if _, ok := v["pad"]; !ok {
		t.Fatalf("body did not decode: %#v", v)
	}
}

// TestBodyBetween32And64MiBNowSucceeds is the behaviour change contract Part
// 3 flags: a valid 40 MiB body is truncated at 32 MiB today and fails to
// decode. It is the only assertion that proves the truncation is gone.
func TestBodyBetween32And64MiBNowSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a 40 MiB body")
	}
	serveBody(t, 200, 40<<20)

	v, err := api.TryGet("/library/sections/1/all", nil)
	if err != nil {
		t.Fatalf("a 40 MiB body failed: %v", err)
	}
	if _, ok := v["pad"]; !ok {
		t.Fatalf("body did not decode: %#v", v)
	}
}

// TestOversizeOn401KeepsAuthCode pins contract 2.3's ordering rule: the HTTP
// status is classified first, so an oversize body never converts a 4xx or a
// 5xx into a decode error. There are no bytes to build a message from, so
// the status reason stands alone.
func TestOversizeOn401KeepsAuthCode(t *testing.T) {
	serveBody(t, 401, api.BodyLimit+1)

	out, code := testutil.Capture(t, func() { api.Get("/x", nil) })
	if code != 5 {
		t.Fatalf("exit = %d, want 5; out=%s", code, out)
	}
	if !strings.Contains(out, `"code":"PLEX_AUTH_REQUIRED"`) || !strings.Contains(out, `"http_status":401`) {
		t.Fatalf("want PLEX_AUTH_REQUIRED with http_status 401, got %s", out)
	}
	if strings.Contains(out, "exceeds the") {
		t.Fatalf("the oversize message leaked past the status classification: %s", out)
	}
}

// TestPartWayReadFailureKeepsTargetCode is the guard on the C3a decision
// that only Oversize and Decode move to DECODE_ERROR. A body read that dies
// part-way is a genuine transport failure and keeps its target's code — and
// it has to be asserted, because the shared classifier maps
// io.ErrUnexpectedEOF to cause.Decode. The only thing keeping this a
// transport failure is that api.Error.Cause() returns the stored field
// verbatim with no sniffing fallback, and that a read failure never sets it.
//
// Distinct from TestTruncatedBodyIsDecodeError, which is a complete response
// carrying invalid JSON. Both produce "unexpected EOF" text; only one of
// them is a decode error.
func TestPartWayReadFailureKeepsTargetCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"a":`))
		// Kill the connection mid-body: the client has a Content-Length it
		// will never see satisfied.
		panic(http.ErrAbortHandler)
	}))
	defer srv.Close()
	testutil.Setup(t, srv.URL)

	out, code := testutil.Capture(t, func() { api.Get("/x", nil) })
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (transport); out=%s", code, out)
	}
	if !strings.Contains(out, `"code":"TRANSPORT_FAILED"`) {
		t.Fatalf("want TRANSPORT_FAILED, got %s", out)
	}
	if strings.Contains(out, "DECODE_ERROR") {
		t.Fatalf("a part-way read failure became a decode error: %s", out)
	}
}
