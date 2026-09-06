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

// Stdout, Stderr and Exit are seams for tests; production code never
// overrides them. Stderr carries exactly one thing: the plain-text fallback
// line when the error envelope itself cannot be written.
var (
	Stdout io.Writer = os.Stdout
	Stderr io.Writer = os.Stderr
	Exit   func(int) = os.Exit
)

// Print emits one JSON line with no exit-code check — for cli paths that
// bypass _out in the Python original (search, ndjson rows, --json shortcuts).
//
// It returns the write error rather than discarding it. A write that failed
// must never be reported as success (contract 2.6), so no caller may ignore
// this: use PrintOrFail where there is nothing else to do with it.
func Print(result jsonx.J) error {
	_, err := fmt.Fprintln(Stdout, jsonx.Marshal(result))
	return err
}

// PrintOrFail prints and, on a write failure, reports it as INTERNAL at exit
// 4 through the same path Out uses. For callers whose print is the last thing
// they do and which have nowhere to return an error to.
func PrintOrFail(result jsonx.J) {
	if err := Print(result); err != nil {
		failWrite(err)
	}
}

// failWrite is the one response to a failed stdout write: an INTERNAL
// envelope at exit 4. FailErr handles the case where that write fails too.
func failWrite(err error) {
	FailErr(Err(CodeInternal, "could not write output: "+err.Error()))
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
	if err := Print(result); err != nil {
		failWrite(err)
	}
}

// EmitNDJSON mirrors cli._emit_ndjson: one JSON object per row as produced
// (each Fprintln is an unbuffered write, so a killed caller keeps partial
// progress), then the summary line with "count" filled in.
// A write failure stops the run at the failing row: rows already written
// stand, nothing further is written, and no summary line is emitted — a
// summary after a lost row would misreport the count (contract 2.6).
func EmitNDJSON(rows iter.Seq[jsonx.J], summary jsonx.J) {
	count := 0
	failed := error(nil)
	for row := range rows {
		if err := Print(row); err != nil {
			failed = err
			break
		}
		count++
	}
	if failed != nil {
		failWrite(failed)
		return
	}
	summary["count"] = count
	if err := Print(summary); err != nil {
		failWrite(err)
	}
}
