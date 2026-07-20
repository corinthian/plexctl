// Package output owns the stdout JSON contract and exit-code discipline.
// Every path through this package writes exactly one line of JSON to stdout;
// failures carry a code from the closed enumeration in errors.go, with the
// exit class (0–6, see docs/error_model_v2.md) derived from the code. NDJSON
// commands emit many lines by design — one per row plus a summary — which is
// not an exception to "one line," just a caller that calls Print repeatedly.
// The one deliberate exception is cobra's own --help/--version handling,
// which bypasses this package entirely and prints non-JSON text at exit 0.
package output

import (
	"fmt"
	"io"
	"iter"
	"os"

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

// Out emits a success result. Failures never come here in v2 — they go
// through FailErr with a coded CLIError. The falsy-ok branch is a canary:
// any straggler still emitting a v1 free-text failure envelope surfaces
// loudly as an INTERNAL bug instead of silently keeping the old contract.
func Out(result jsonx.J) {
	if !jsonx.Truthy(result["ok"]) {
		errStr, _ := result["error"].(string)
		FailErr(Err(CodeInternal, "uncoded failure envelope reached output.Out — plexctl bug: "+errStr))
		return
	}
	Print(result)
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
