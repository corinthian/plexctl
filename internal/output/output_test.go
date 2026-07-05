package output_test

import (
	"iter"
	"strings"
	"testing"

	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/testutil"
)

func TestOutOKExitsZero(t *testing.T) {
	out, code := testutil.Capture(t, func() {
		output.Out(jsonx.J{"ok": true})
	})
	if code != -1 {
		t.Fatalf("ok result should not exit, got %d", code)
	}
	if strings.TrimSpace(out) != `{"ok":true}` {
		t.Fatalf("out = %q", out)
	}
}

func TestOutErrorExitsOne(t *testing.T) {
	_, code := testutil.Capture(t, func() {
		output.Out(jsonx.J{"ok": false, "error": "connection failed: nope"})
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestOutTimeoutExitsTwo(t *testing.T) {
	// The companion layer builds error dicts by hand; _out matches the stable
	// message prefix to preserve the exit-2 retry contract.
	_, code := testutil.Capture(t, func() {
		output.Out(jsonx.J{"ok": false, "error": "request timed out: x"})
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestEmitNDJSON(t *testing.T) {
	rows := func(yield func(jsonx.J) bool) {
		for _, title := range []string{"a", "b"} {
			if !yield(jsonx.J{"title": title}) {
				return
			}
		}
	}
	out, code := testutil.Capture(t, func() {
		output.EmitNDJSON(iter.Seq[jsonx.J](rows), jsonx.J{"ok": true})
	})
	if code != -1 {
		t.Fatalf("ndjson should not exit, got %d", code)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 2 rows + summary, got %d lines: %q", len(lines), out)
	}
	if lines[2] != `{"count":2,"ok":true}` {
		t.Fatalf("summary = %q", lines[2])
	}
}
