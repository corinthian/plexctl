// Package output owns the stdout JSON contract and exit-code discipline.
// Every path through this package writes exactly one line of JSON to stdout
// and exits 0 on success, 1 on failure, 2 on request timeout, 64 on a usage
// or validation error. NDJSON commands emit many lines by design — one per
// row plus a summary — which is not an exception to "one line," just a
// caller that calls Print repeatedly. The one deliberate exception is
// cobra's own --help/--version handling, which bypasses this package
// entirely and prints non-JSON text at exit 0.
package output

import (
	"fmt"
	"io"
	"iter"
	"os"
	"strings"

	"github.com/corinthian/plexctl/internal/jsonx"
)

// Stdout and Exit are seams for tests; production code never overrides them.
var (
	Stdout io.Writer = os.Stdout
	Exit   func(int) = os.Exit
)

// Print emits one JSON line with no exit-code check — for cli paths that
// bypass _out in the Python original (search, ndjson rows, --json shortcuts).
func Print(result jsonx.J) {
	fmt.Fprintln(Stdout, jsonx.Marshal(result))
}

// Out mirrors cli._out: print the result, then exit 1 on falsy "ok", or 2
// when the error carries the stable timeout prefix (batch callers retry
// exit-2 items only).
func Out(result jsonx.J) {
	Print(result)
	if !jsonx.Truthy(result["ok"]) {
		errStr, _ := result["error"].(string)
		if strings.HasPrefix(errStr, "request timed out") {
			Exit(2)
		} else {
			Exit(1)
		}
	}
}

// Fail prints the standard error envelope and exits 1 (config/bootstrap
// failures that are never timeouts).
func Fail(msg string) {
	Print(jsonx.J{"ok": false, "error": msg})
	Exit(1)
}

// Usage prints the standard error envelope and exits 64 (EX_USAGE): a
// malformed invocation — bad flag value, empty required argument — that a
// retry can never fix without the caller changing the command.
func Usage(msg string) {
	Print(jsonx.J{"ok": false, "error": msg})
	Exit(64)
}

// EmitNDJSON mirrors cli._emit_ndjson: one JSON object per row as produced
// (each Fprintln is an unbuffered write, so a killed caller keeps partial
// progress), then the summary line with "count" filled in.
func EmitNDJSON(rows iter.Seq[jsonx.J], summary jsonx.J) {
	count := 0
	for row := range rows {
		Print(row)
		count++
	}
	summary["count"] = count
	Print(summary)
}
