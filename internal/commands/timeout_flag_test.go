package commands_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/testutil"
)

// runCLI drives the real entry point so the exit code is the one a user
// would see. Every case here is rejected in PersistentPreRunE or in seek's
// own argument loop, before any request, so no fake PMS is needed — but the
// config directory is still redirected, because nothing in a test may reach
// ~/.config/plexctl.
func runCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	testutil.Setup(t, "http://127.0.0.1:1")
	t.Setenv("PLEXCTL_TIMEOUT", "")
	t.Cleanup(api.ClearTimeoutForTest)
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = append([]string{"plexctl"}, args...)
	return testutil.Capture(t, commands.Execute)
}

// TestParseTimeoutRejects pins contract 2.1's grammar on the flag surface:
// whole seconds, 1 to 86400, no sign, no separators, no unit suffix, no
// trimming. Every one of these is accepted today by the Float64Var.
func TestParseTimeoutRejects(t *testing.T) {
	for _, raw := range []string{"10.5", "NaN", "Inf", "+Inf", "1e18", "1e-12", "90000", "86401", "30s", " 30 ", "+30", "0x1e", "1_0"} {
		t.Run(raw, func(t *testing.T) {
			out, code := runCLI(t, "--timeout", raw, "now-playing")
			if code != 1 {
				t.Fatalf("--timeout %q: exit %d, want 1 (BAD_REQUEST); out=%s", raw, code, out)
			}
			if !strings.Contains(out, `"code":"BAD_REQUEST"`) {
				t.Fatalf("--timeout %q: want BAD_REQUEST, got %s", raw, out)
			}
			if !strings.Contains(out, "--timeout") {
				t.Fatalf("--timeout %q: message does not name the source: %s", raw, out)
			}
		})
	}
}

// TestParseTimeoutAccepts is the other half: the grammar's accepted forms
// still resolve, leading zeros included.
func TestParseTimeoutAccepts(t *testing.T) {
	for raw, want := range map[string]int{"1": 1, "30": 30, "00030": 30, "86400": 86400} {
		t.Run(raw, func(t *testing.T) {
			got, err := api.ResolveTimeout(true, raw)
			if err != nil {
				t.Fatalf("--timeout %s rejected: %v", raw, err)
			}
			if int(got.Seconds()) != want {
				t.Fatalf("--timeout %s resolved to %v, want %ds", raw, got, want)
			}
		})
	}
}

// TestEmptyTimeoutFlagIsBadRequest is the regression guard on the resolution
// shape. `--timeout ""` is BAD_REQUEST today only because cobra's Float64Var
// refuses to parse an empty string; moving to a StringVar removes that
// accident, and routing the flag through xduration.Resolve — which skips an
// empty candidate as unset — would silently fall through to the environment,
// the config and the default and exit 0. The flag is parsed directly instead.
func TestEmptyTimeoutFlagIsBadRequest(t *testing.T) {
	cases := [][]string{
		{"--timeout", "", "now-playing"},
		{"--timeout=", "now-playing"},
		{"seek", "--timeout", "", "1:30"},
		{"seek", "--timeout=", "1:30"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, code := runCLI(t, args...)
			if code != 1 {
				t.Fatalf("%v: exit %d, want 1; out=%s", args, code, out)
			}
			if !strings.Contains(out, `"code":"BAD_REQUEST"`) || !strings.Contains(out, "--timeout") {
				t.Fatalf("%v: want a BAD_REQUEST naming --timeout, got %s", args, out)
			}
		})
	}
}

// TestSeekTimeoutUsesTheSameParser pins contract 2.1's plexctl exception:
// seek's DisableFlagParsing means it hand-parses --timeout, and it must do
// so through the same parser with the same source name rather than the
// hand-rolled ParseFloat it used to carry.
func TestSeekTimeoutUsesTheSameParser(t *testing.T) {
	cases := [][]string{
		{"seek", "--timeout", "10.5", "1:30"},
		{"seek", "--timeout=10.5", "1:30"},
		{"seek", "--timeout", "0", "1:30"},
		{"seek", "--timeout", "30s", "1:30"},
		{"seek", "--timeout", "90000", "1:30"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, code := runCLI(t, args...)
			if code != 1 {
				t.Fatalf("%v: exit %d, want 1; out=%s", args, code, out)
			}
			if !strings.Contains(out, `"code":"BAD_REQUEST"`) || !strings.Contains(out, "--timeout") {
				t.Fatalf("%v: want a BAD_REQUEST naming --timeout, got %s", args, out)
			}
		})
	}
}

// TestCorruptConfigDoesNotBreakAuthLogin pins contract Part 3's plexctl row
// "Config unparseable, auth login → login quarantines and merges, exit 0",
// which is marked unchanged. Resolving the timeout in PersistentPreRunE runs
// before every RunE, auth login's included, so reading the config there
// through the print-and-exit config.Load would abort at PLEX_AUTH_REQUIRED 5
// before login's quarantine-and-merge repair ever ran — the one command whose
// whole job is to fix an unusable config.
//
// configTimeoutRaw uses config.TryLoad and treats a load failure as no
// candidate. Nothing is lost: a file that cannot be parsed has no readable
// timeout in it either, and every genuine config-failure row still fires
// where the config is actually needed — config.Require and api.Request's own
// load.
func TestCorruptConfigDoesNotBreakAuthLogin(t *testing.T) {
	dir := testutil.Setup(t, "http://127.0.0.1:1")
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("not toml at all ][\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLEXCTL_TIMEOUT", "")

	// `commands` reads no config of its own, so PersistentPreRunE is the only
	// thing that could touch the file. It must complete.
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"plexctl", "commands"}
	out, code := testutil.Capture(t, commands.Execute)
	if code == 5 {
		t.Fatalf("a corrupt config aborted in PersistentPreRunE at exit 5; out=%s", out)
	}
	if strings.Contains(out, "PLEX_AUTH_REQUIRED") {
		t.Fatalf("timeout resolution routed a corrupt config through the auth code: %s", out)
	}

	// And the timeout still resolves to the default rather than failing.
	d, err := api.ResolveTimeout(false, "")
	if err != nil {
		t.Fatalf("a corrupt config made timeout resolution an error: %v", err)
	}
	if d != api.DefaultTimeout {
		t.Fatalf("timeout = %v, want the default %v", d, api.DefaultTimeout)
	}
}
