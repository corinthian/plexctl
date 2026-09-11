package output

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/corinthian/plexctl/internal/jsonx"
)

// failWriter fails on the Nth write and passes every other one through.
//
// N matters. An always-failing writer takes the INTERNAL envelope's own
// write down with it, and the test can no longer assert the thing it is
// named for. On a genuinely dead stdout the two contract rows compose to the
// stderr line plus exit 4, which TestErrorPathWriteFailureExitsFour covers;
// the exit is 4 either way.
type failWriter struct {
	buf   bytes.Buffer
	n     int
	count int
}

var errWriteFailed = errors.New("no space left on device")

func (w *failWriter) Write(p []byte) (int, error) {
	w.count++
	if w.count == w.n {
		return 0, errWriteFailed
	}
	return w.buf.Write(p)
}

// captureWrites swaps all three seams and returns what reached stdout, what
// reached stderr, and the exit code (-1 when Exit was never called).
func captureWrites(t *testing.T, w io.Writer, f func()) (string, string, int) {
	t.Helper()
	var ebuf bytes.Buffer
	exit := -1
	oldOut, oldErr, oldExit := Stdout, Stderr, Exit
	Stdout, Stderr, Exit = w, &ebuf, func(c int) { exit = c }
	defer func() { Stdout, Stderr, Exit = oldOut, oldErr, oldExit }()
	f()
	if fw, ok := w.(*failWriter); ok {
		return fw.buf.String(), ebuf.String(), exit
	}
	return "", ebuf.String(), exit
}

// TestPrintReturnsWriteError is the unit assertion that Print no longer
// swallows the Fprintln error.
func TestPrintReturnsWriteError(t *testing.T) {
	w := &failWriter{n: 1}
	captureWrites(t, w, func() {
		if err := Print(jsonx.J{"ok": true}); !errors.Is(err, errWriteFailed) {
			t.Fatalf("Print returned %v, want the write error", err)
		}
	})
}

// TestSuccessWriteFailureIsInternal pins contract 2.6's success row: a write
// that failed is never reported as ok:true. The envelope was discarded at
// output.go:29 before this.
func TestSuccessWriteFailureIsInternal(t *testing.T) {
	w := &failWriter{n: 1}
	out, _, exit := captureWrites(t, w, func() {
		Out(jsonx.J{"ok": true, "title": "something"})
	})
	if exit != ExitInternal {
		t.Fatalf("exit = %d, want %d", exit, ExitInternal)
	}
	if !strings.Contains(out, `"code":"INTERNAL"`) {
		t.Fatalf("want an INTERNAL envelope on stdout, got %q", out)
	}
	if strings.Contains(out, `"ok":true`) {
		t.Fatalf("ok:true was reported after a failed write: %q", out)
	}
}

// TestErrorPathWriteFailureExitsFour pins the error row: the envelope is not
// retried, one plain-text line goes to stderr, and the exit is 4 whatever
// the original code's class was.
func TestErrorPathWriteFailureExitsFour(t *testing.T) {
	w := &failWriter{n: 1}
	out, errOut, exit := captureWrites(t, w, func() {
		FailErr(Err(CodeTransportTimeout, "request timed out"))
	})
	if exit != ExitInternal {
		t.Fatalf("exit = %d, want %d (not the original code's class %d)", exit, ExitInternal, ExitTransport)
	}
	if out != "" {
		t.Fatalf("nothing should have reached stdout, got %q", out)
	}
	want := "plexctl: cannot write output: no space left on device (original error: TRANSPORT_TIMEOUT)\n"
	if errOut != want {
		t.Fatalf("stderr = %q, want %q", errOut, want)
	}
	if strings.Count(errOut, "\n") != 1 {
		t.Fatalf("want exactly one stderr line, got %q", errOut)
	}
}

// TestNDJSONStopsAtFailingRow pins the mid-stream row: rows already written
// stand, nothing further is written, and no summary line is emitted — a
// summary after a lost row would misreport the count.
func TestNDJSONStopsAtFailingRow(t *testing.T) {
	w := &failWriter{n: 3}
	rows := func(yield func(jsonx.J) bool) {
		for i := 1; i <= 5; i++ {
			if !yield(jsonx.J{"row": i}) {
				return
			}
		}
	}
	out, _, exit := captureWrites(t, w, func() {
		EmitNDJSON(rows, jsonx.J{"ok": true, "summary": true})
	})
	if exit != ExitInternal {
		t.Fatalf("exit = %d, want %d", exit, ExitInternal)
	}
	if !strings.Contains(out, `"row":1`) || !strings.Contains(out, `"row":2`) {
		t.Fatalf("rows already written should stand: %q", out)
	}
	for _, gone := range []string{`"row":3`, `"row":4`, `"row":5`} {
		if strings.Contains(out, gone) {
			t.Fatalf("%s was written after the failure: %q", gone, out)
		}
	}
	if strings.Contains(out, `"summary":true`) || strings.Contains(out, `"count"`) {
		t.Fatalf("a summary line was emitted after a lost row: %q", out)
	}
	if !strings.Contains(out, `"code":"INTERNAL"`) {
		t.Fatalf("want the INTERNAL envelope: %q", out)
	}
}

// TestWarningsAreNeverAWriteFailure: contract 2.6 says a write failure is
// never a warning, and the warnings array is unchanged.
func TestWarningsAreNeverAWriteFailure(t *testing.T) {
	w := &failWriter{n: 1}
	out, _, exit := captureWrites(t, w, func() {
		Out(Warn(jsonx.J{"ok": true}, CodeStateSaveFailed, "state write failed", ""))
	})
	if exit != ExitInternal {
		t.Fatalf("exit = %d, want %d", exit, ExitInternal)
	}
	if strings.Contains(out, "cannot write output") && strings.Contains(out, `"warnings"`) {
		t.Fatalf("the write failure was folded into warnings: %q", out)
	}
}
