package commands_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/testutil"
)

// Two hidden probe commands, so Execute can be driven with a RunE that
// returns each shape. Hidden keeps them out of `plexctl commands` (discovery
// skips Hidden) and out of help, so no discovery or golden test sees them.
func init() {
	commands.Register(func(root *cobra.Command) {
		root.AddCommand(&cobra.Command{
			Use:    "probe-typed-error",
			Hidden: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				return output.Err(output.CodeTransportFailed, "connection failed: probe")
			},
		})
		root.AddCommand(&cobra.Command{
			Use:    "probe-plain-error",
			Hidden: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				return errors.New("something the validator rejected")
			},
		})
	})
}

func runExecute(t *testing.T, args ...string) (string, int) {
	t.Helper()
	testutil.Setup(t, "http://127.0.0.1:1")
	t.Setenv("PLEXCTL_TIMEOUT", "")
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = append([]string{"plexctl"}, args...)
	return testutil.Capture(t, commands.Execute)
}

// TestExecutePreservesTypedError pins the change: Execute rewrote every
// error a RunE returned into BAD_REQUEST at exit 1, including one that
// already carried its own code. A domain failure that chose to return rather
// than call output.FailErr was silently relabelled a usage error.
func TestExecutePreservesTypedError(t *testing.T) {
	out, code := runExecute(t, "probe-typed-error")
	if code != 3 {
		t.Fatalf("exit = %d, want 3; out=%s", code, out)
	}
	if !strings.Contains(out, `"code":"TRANSPORT_FAILED"`) {
		t.Fatalf("want TRANSPORT_FAILED, got %s", out)
	}
	if strings.Contains(out, "BAD_REQUEST") {
		t.Fatalf("the typed error was rewritten: %s", out)
	}
}

// TestExecuteStillMapsPlainErrorsToBadRequest pins the half that does not
// change: cobra's own rejections and every hand-rolled validator that
// returns a plain error still become BAD_REQUEST at exit 1.
func TestExecuteStillMapsPlainErrorsToBadRequest(t *testing.T) {
	cases := map[string][]string{
		"hand-rolled validator": {"probe-plain-error"},
		"unknown flag":          {"now-playing", "--no-such-flag"},
		"unknown command":       {"no-such-command"},
		"arg count":             {"metadata"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			out, code := runExecute(t, args...)
			if code != 1 {
				t.Fatalf("exit = %d, want 1; out=%s", code, out)
			}
			if !strings.Contains(out, `"code":"BAD_REQUEST"`) {
				t.Fatalf("want BAD_REQUEST, got %s", out)
			}
		})
	}
}

// TestSeekTimeoutErrorExitsBadRequest: the timeout parser's error travels
// back through a RunE, and plexctl maps every timeout rejection to
// BAD_REQUEST whatever the source — so preserving typed errors must not
// change where this one lands.
func TestSeekTimeoutErrorExitsBadRequest(t *testing.T) {
	out, code := runExecute(t, "seek", "--timeout", "10.5", "1:30")
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out=%s", code, out)
	}
	if !strings.Contains(out, `"code":"BAD_REQUEST"`) || !strings.Contains(out, "--timeout") {
		t.Fatalf("want a BAD_REQUEST naming --timeout, got %s", out)
	}
}
