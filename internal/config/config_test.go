package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/corinthian/plexctl/internal/config"
	"github.com/corinthian/plexctl/internal/testutil"
)

func TestSaveLoadRoundTripWithEscaping(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PLEXCTL_CONFIG_DIR", dir)
	err := config.Save([]config.KV{
		{K: "server_url", V: "http://10.0.0.2:32400"},
		{K: "token", V: `we"ird\token`},
		{K: "default_client", V: "Apple TV"},
		{K: "client_id", V: "plexctl-abc12345"},
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	cfg := config.Load()
	if cfg["token"] != `we"ird\token` {
		t.Fatalf("token round-trip = %q", cfg["token"])
	}
	if cfg["server_url"] != "http://10.0.0.2:32400" {
		t.Fatalf("server_url = %q", cfg["server_url"])
	}
}

// TestSaveWritesViaTempRenameNoLeftoverTmp pins W12: Save writes through a
// temp file and renames it into place, like every other writer in this
// codebase, instead of writing config.toml in place while every command
// reads it unlocked. The temp name is now generated rather than fixed, so
// this globs the directory — statting config.toml.tmp would pass while
// proving nothing, and the no-leftover guarantee is what makes the
// os.Remove deferred on every non-rename exit load-bearing.
func TestSaveWritesViaTempRenameNoLeftoverTmp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PLEXCTL_CONFIG_DIR", dir)
	if err := config.Save([]config.KV{{K: "token", V: "tok"}}); err != nil {
		t.Fatal(err)
	}
	leftovers, err := filepath.Glob(filepath.Join(config.Dir(), "config.toml.*tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("leftover temp files after Save: %v", leftovers)
	}
	if _, err := os.Stat(config.Path()); err != nil {
		t.Fatalf("config.toml missing after Save: %v", err)
	}
}

func TestLoadReadsPythonWriterFormat(t *testing.T) {
	// Pinned byte-format: bare `k = "v"` lines.
	dir := t.TempDir()
	t.Setenv("PLEXCTL_CONFIG_DIR", dir)
	py := "server_url = \"http://10.0.0.2:32400\"\ntoken = \"tok\"\ndefault_client = \"Apple TV\"\nclient_id = \"plexctl-deadbeef\"\ntimeout = \"8\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(py), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Load()
	if cfg["client_id"] != "plexctl-deadbeef" || cfg["timeout"] != "8" {
		t.Fatalf("unexpected load: %#v", cfg)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	t.Setenv("PLEXCTL_CONFIG_DIR", t.TempDir())
	if got := config.Load(); len(got) != 0 {
		t.Fatalf("want empty map, got %#v", got)
	}
}

// TestLoadInvalidTOMLExitsFive pins the v2 error-model contract (P2-A):
// corrupt config is PLEX_AUTH_REQUIRED (the caller isn't authenticated with
// a usable config), exit 5 — not the v1 free-text exit 1.
func TestLoadInvalidTOMLExitsFive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PLEXCTL_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("not = = toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := testutil.Capture(t, func() { config.Load() })
	if code != 5 {
		t.Fatalf("exit = %d, want 5", code)
	}
	if !strings.Contains(out, `"code":"PLEX_AUTH_REQUIRED"`) {
		t.Fatalf("error code drifted: %q", out)
	}
	if !strings.Contains(out, "invalid config at") || !strings.Contains(out, "run plexctl auth login") {
		t.Fatalf("error message drifted: %q", out)
	}
	if !strings.Contains(out, `"hint":"run: plexctl auth login"`) {
		t.Fatalf("hint drifted: %q", out)
	}
}

// TestTryLoadInvalidTOMLReturnsErrorInsteadOfExiting pins W5's second
// trap: Load's print-and-exit on malformed TOML would make `auth login`
// abort while trying to repair the very file that's corrupt. TryLoad
// reports the error instead of exiting, so the merge step can tolerate it.
func TestTryLoadInvalidTOMLReturnsErrorInsteadOfExiting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PLEXCTL_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("not = = toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := testutil.Capture(t, func() {
		m, err := config.TryLoad()
		if err == nil {
			t.Fatal("expected a parse error, got nil")
		}
		if m != nil {
			t.Fatalf("map = %#v, want nil on error", m)
		}
	})
	if code != -1 {
		t.Fatalf("exit = %d, want -1 (TryLoad never calls output.Exit); out=%s", code, out)
	}
}

// TestSaveTightensDirMode pins W3 (finding 6): Save must both create a
// fresh config dir private (MkdirAll 0700) and migrate an older,
// world-readable dir left over from before this fix (explicit Chmod —
// MkdirAll never tightens a pre-existing directory's mode on its own).
func TestSaveTightensDirMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cfg") // does not exist yet
	t.Setenv("PLEXCTL_CONFIG_DIR", dir)
	if err := config.Save([]config.KV{{K: "token", V: "tok"}}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(config.Dir()); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("fresh dir mode = %o, err=%v, want 0700", info.Mode().Perm(), err)
	}

	if err := os.Chmod(config.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.Save([]config.KV{{K: "token", V: "tok2"}}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(config.Dir()); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("migrated dir mode = %o, err=%v, want 0700 (Save must re-tighten an older 0755 dir)", info.Mode().Perm(), err)
	}
}

// TestRequireMissingExitsFive pins the v2 error-model contract (P2-A):
// missing config key is PLEX_AUTH_REQUIRED, exit 5 — not the v1 free-text
// exit 1.
func TestRequireMissingExitsFive(t *testing.T) {
	t.Setenv("PLEXCTL_CONFIG_DIR", t.TempDir())
	out, code := testutil.Capture(t, func() { config.Require("token") })
	if code != 5 {
		t.Fatalf("exit = %d, want 5", code)
	}
	if !strings.Contains(out, `"code":"PLEX_AUTH_REQUIRED"`) {
		t.Fatalf("error code drifted: %q", out)
	}
	if !strings.Contains(out, "missing config key: token — run plexctl auth login") {
		t.Fatalf("error message drifted: %q", out)
	}
	if !strings.Contains(out, `"hint":"run: plexctl auth login"`) {
		t.Fatalf("hint drifted: %q", out)
	}
}

// TestSaveNewlineValueRoundTrips inverts the reproduction for the
// hand-rolled writer: it escaped backslashes and double quotes only, so any
// other TOML-significant byte in a value produced a file the CLI could no
// longer parse. A newline is the reachable case — a client named
// "Living\nRoom" wrote a broken config.toml and bricked every later command
// with PLEX_AUTH_REQUIRED.
func TestSaveNewlineValueRoundTrips(t *testing.T) {
	t.Setenv("PLEXCTL_CONFIG_DIR", t.TempDir())
	if err := config.Save([]config.KV{{K: "default_client", V: "Living\nRoom"}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.TryLoad()
	if err != nil {
		t.Fatalf("saved config no longer parses: %v", err)
	}
	if cfg["default_client"] != "Living\nRoom" {
		t.Fatalf("default_client = %#v, want the newline value back intact", cfg["default_client"])
	}
}

// TestSaveLoadRoundTripTypes pins that Save preserves TOML types instead of
// stringifying everything. Compare against post-round-trip types: TOML
// integers come back as int64 and floats as float64, so the expectations
// below are the decoded forms, not the literals passed in.
func TestSaveLoadRoundTripTypes(t *testing.T) {
	t.Setenv("PLEXCTL_CONFIG_DIR", t.TempDir())
	pairs := []config.KV{
		{K: "token", V: "we\"ird\\token\nwith a newline"},
		{K: "quoted.dotted key", V: "kept whole"},
		{K: "timeout", V: 10},
		{K: "ratio", V: 1.5},
		{K: "verbose", V: true},
		{K: "langs", V: []string{"eng", "jpn"}},
		{K: "section", V: map[string]any{"nested": "value", "n": 2}},
	}
	if err := config.Save(pairs); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.TryLoad()
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"token":             "we\"ird\\token\nwith a newline",
		"quoted.dotted key": "kept whole",
		"timeout":           int64(10),
		"ratio":             1.5,
		"verbose":           true,
		"langs":             []any{"eng", "jpn"},
		"section":           map[string]any{"nested": "value", "n": int64(2)},
	}
	for k, w := range want {
		if got := cfg[k]; !reflect.DeepEqual(got, w) {
			t.Fatalf("%s = %#v (%T), want %#v (%T)", k, got, got, w, w)
		}
	}
	if len(cfg) != len(want) {
		t.Fatalf("round-tripped %d keys, want %d: %#v", len(cfg), len(want), cfg)
	}
}

// TestSaveDoesNotFollowTmpSymlink inverts the second reproduction: Save
// wrote to the fixed path config.toml.tmp, so a symlink pre-planted there
// by anything else with write access to the config dir was followed and its
// target overwritten with the file about to hold the Plex token.
// os.CreateTemp creates a fresh, unpredictable name with O_EXCL instead.
func TestSaveDoesNotFollowTmpSymlink(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PLEXCTL_CONFIG_DIR", dir)
	sibling := filepath.Join(dir, "other")
	if err := os.WriteFile(sibling, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sibling, config.Path()+".tmp"); err != nil {
		t.Fatal(err)
	}

	if err := config.Save([]config.KV{{K: "token", V: "tok"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "original" {
		t.Fatalf("symlink target = %q, want %q — Save followed the planted .tmp symlink", b, "original")
	}
	cfg, err := config.TryLoad()
	if err != nil {
		t.Fatal(err)
	}
	if cfg["token"] != "tok" {
		t.Fatalf("token = %#v, want the saved value", cfg["token"])
	}
}
