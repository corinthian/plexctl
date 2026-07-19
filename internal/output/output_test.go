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

func TestOutFalsyOkIsInternalCanary(t *testing.T) {
	// v2: failures never reach Out — a falsy-ok envelope here is a plexctl
	// bug and must surface loudly as INTERNAL (exit 4), not silently keep the
	// v1 free-text contract.
	out, code := testutil.Capture(t, func() {
		output.Out(jsonx.J{"ok": false, "error": "connection failed: nope"})
	})
	if code != output.ExitInternal {
		t.Fatalf("exit = %d, want %d", code, output.ExitInternal)
	}
	if !strings.Contains(out, `"code":"INTERNAL"`) || !strings.Contains(out, "uncoded failure envelope") {
		t.Fatalf("canary envelope drifted: %q", out)
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
