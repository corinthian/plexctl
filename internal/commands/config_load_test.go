package commands_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/config"
	"github.com/corinthian/plexctl/internal/testutil"
)

// runRoot drives one whole invocation through the real root, the way a user
// would reach it, and reports what was printed and the exit code. The config
// directory is always redirected first: nothing in a test may reach
// ~/.config/plexctl.
func runRoot(t *testing.T, args ...string) (string, int) {
	t.Helper()
	root := commands.BuildRoot()
	root.SetArgs(args)
	root.SetOut(new(strings.Builder))
	root.SetErr(new(strings.Builder))
	return testutil.Capture(t, func() { _ = root.Execute() })
}

// writeUnparseableConfig replaces the config file with something no TOML
// parser will accept. Any read of it is therefore visible twice over: in the
// counter, and in a PLEX_AUTH_REQUIRED envelope if the read is the
// print-and-exit one.
func writeUnparseableConfig(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("not toml at all ][\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestConfigReadOncePerInvocation is contract 2.7's "load frequency only".
// A single PMS-backed command reads the config three times today — once in
// ServerBase, once for the request's client_id and once through
// config.Require for the token — and must read it once.
func TestConfigReadOncePerInvocation(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/status/sessions", map[string]any{"MediaContainer": map[string]any{}})
	t.Setenv("PLEXCTL_TIMEOUT", "")

	config.ResetReads()
	out, code := runRoot(t, "now-playing")
	if code != -1 {
		t.Fatalf("now-playing exited %d; out=%s", code, out)
	}
	if got := config.Reads(); got != 1 {
		t.Fatalf("config read %d times in one invocation, want 1; out=%s", got, out)
	}
}

// TestHelpNeverReadsConfig, TestCommandsListNeverReadsConfig and
// TestArgumentErrorNeverReadsConfig are contract Part 3's plexctl row: help,
// discovery and argument errors never touch the file. Each runs with a
// config that would fail to parse, so a read is not merely counted, it is
// fatal to the invocation if anything takes the print-and-exit path.
func TestHelpNeverReadsConfig(t *testing.T) {
	dir := testutil.Setup(t, "http://127.0.0.1:1")
	writeUnparseableConfig(t, dir)
	t.Setenv("PLEXCTL_TIMEOUT", "")

	config.ResetReads()
	out, code := runRoot(t, "--help")
	if code != -1 {
		t.Fatalf("--help exited %d; out=%s", code, out)
	}
	if got := config.Reads(); got != 0 {
		t.Fatalf("--help read the config %d times, want 0", got)
	}
}

func TestCommandsListNeverReadsConfig(t *testing.T) {
	dir := testutil.Setup(t, "http://127.0.0.1:1")
	writeUnparseableConfig(t, dir)
	t.Setenv("PLEXCTL_TIMEOUT", "")

	config.ResetReads()
	out, code := runRoot(t, "commands")
	if code != -1 {
		t.Fatalf("commands exited %d; out=%s", code, out)
	}
	if strings.Contains(out, "PLEX_AUTH_REQUIRED") {
		t.Fatalf("commands routed a corrupt config through the auth code: %s", out)
	}
	if got := config.Reads(); got != 0 {
		t.Fatalf("commands read the config %d times, want 0", got)
	}
}

func TestArgumentErrorNeverReadsConfig(t *testing.T) {
	dir := testutil.Setup(t, "http://127.0.0.1:1")
	writeUnparseableConfig(t, dir)
	t.Setenv("PLEXCTL_TIMEOUT", "")

	config.ResetReads()
	// seek takes exactly one POSITION; none is a cobra-level rejection that
	// never reaches a RunE body.
	root := commands.BuildRoot()
	root.SetArgs([]string{"seek"})
	root.SetOut(new(strings.Builder))
	root.SetErr(new(strings.Builder))
	var err error
	testutil.Capture(t, func() { err = root.Execute() })
	if err == nil {
		t.Fatal("seek with no POSITION was accepted")
	}
	if got := config.Reads(); got != 0 {
		t.Fatalf("an argument error read the config %d times, want 0", got)
	}
}
