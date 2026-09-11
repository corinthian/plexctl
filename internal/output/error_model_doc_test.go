package output

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// docRow matches one row of the §2 enumeration table: a backticked code in
// the first column and whatever the second column holds. Only those two
// columns are read — the "fires when", hint and data columns are prose that
// changes every time a behaviour lands, and keying on them would make this
// test break for reasons that have nothing to do with the map.
var docRow = regexp.MustCompile("^\\|\\s*`([A-Z_]+)`\\s*\\|\\s*([^|]*?)\\s*\\|")

// TestCodeExitMatchesErrorModelDoc pins codeExit against the documented
// enumeration in both directions: a code added to the map without a doc row,
// or documented at an exit the map does not agree with, fails here. The
// existing lockstep assertion in TestCodeExitPairs proves test-to-map parity
// only; nothing checked the doc until this test.
func TestCodeExitMatchesErrorModelDoc(t *testing.T) {
	const doc = "../../docs/error_model_v2.md"
	b, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("reading %s: %v", doc, err)
	}
	docExit := map[string]int{}
	warningOnly := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		m := docRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		code, exit := m[1], m[2]
		n, err := strconv.Atoi(exit)
		if err != nil {
			// The warning-only row carries "n/a (warning only)" where an
			// exit would be. It is documented and deliberately absent from
			// codeExit; anything else in that column is a malformed row.
			if !strings.Contains(exit, "warning only") {
				t.Errorf("%s: exit column %q is neither a number nor the warning-only marker", code, exit)
				continue
			}
			warningOnly[code] = true
			continue
		}
		if _, dup := docExit[code]; dup {
			t.Errorf("%s appears twice in the §2 table", code)
		}
		docExit[code] = n
	}
	if len(docExit) == 0 {
		t.Fatalf("parsed no code rows out of %s — the table shape changed", doc)
	}
	for code, want := range docExit {
		got, ok := codeExit[code]
		if !ok {
			t.Errorf("%s is documented at exit %d but is not in codeExit", code, want)
			continue
		}
		if got != want {
			t.Errorf("%s: codeExit says %d, the doc says %d", code, got, want)
		}
	}
	for code := range codeExit {
		if _, ok := docExit[code]; !ok {
			t.Errorf("%s is in codeExit but has no §2 row", code)
		}
	}
	for code := range warningOnly {
		if _, ok := codeExit[code]; ok {
			t.Errorf("%s is documented as warning-only but is in codeExit", code)
		}
	}
	if !warningOnly[CodeStateSaveFailed] {
		t.Errorf("%s is missing its warning-only row in %s", CodeStateSaveFailed, doc)
	}
}
