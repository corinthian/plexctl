package commands_test

import (
	"os"
	"strings"
	"testing"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/testutil"
)

// TestExecuteUnknownFlagExitsUsage pins the exit-code contract's
// highest-leverage line: commands.Execute() — the entry point cmd/plexctl's
// main() actually calls — routes every error cobra's own Execute() returns
// through output.Usage, not just the hand-rolled validators exercised
// elsewhere in this package via root.Execute() directly (which never
// invokes output.Exit at all).
func TestExecuteUnknownFlagExitsUsage(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"plexctl", "--nonexistent-flag"}

	out, code := testutil.Capture(t, commands.Execute)
	if code != 64 {
		t.Fatalf("exit code = %d, want 64; out=%s", code, out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("stdout lines = %d, want exactly 1; out=%s", len(lines), out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("got %#v", got)
	}
}

// TestExecuteHelpAndVersionExitZero pins the package doc's deliberate
// exception: --help/--version are cobra built-ins that return a nil error,
// so commands.Execute() never calls output.Usage/output.Exit for them —
// the process falls through main() and exits 0 naturally.
func TestExecuteHelpAndVersionExitZero(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	for _, args := range [][]string{
		{"plexctl", "--help"},
		{"plexctl", "--version"},
	} {
		os.Args = args
		out, code := testutil.Capture(t, commands.Execute)
		if code != -1 {
			t.Fatalf("%v: exit code = %d, want -1 (no output.Exit call); out=%s", args, code, out)
		}
	}
}

// TestTimeoutFlagNonPositiveExitsUsage pins W1: --timeout 0 used to hang
// forever (http.Client.Timeout of 0 is Go for no timeout). It's now
// rejected at the CLI boundary as a usage error before any request is
// attempted — PersistentPreRunE returns before now-playing's RunE ever
// runs, so this needs no fake PMS.
func TestTimeoutFlagNonPositiveExitsUsage(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	t.Cleanup(func() { api.ClearTimeoutOverride() })

	cases := []struct {
		name string
		args []string
	}{
		{"zero", []string{"plexctl", "--timeout=0", "now-playing"}},
		{"negative", []string{"plexctl", "--timeout=-1", "now-playing"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			os.Args = c.args
			out, code := testutil.Capture(t, commands.Execute)
			if code != 64 {
				t.Fatalf("exit code = %d, want 64; out=%s", code, out)
			}
			got := mustUnmarshal(t, out)
			if got["ok"] != false {
				t.Fatalf("got %#v", got)
			}
		})
	}
}
