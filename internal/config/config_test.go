package config_test

import (
	"os"
	"path/filepath"
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

func TestLoadInvalidTOMLExitsOne(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PLEXCTL_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("not = = toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := testutil.Capture(t, func() { config.Load() })
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "invalid config at") || !strings.Contains(out, "run plexctl auth login") {
		t.Fatalf("error message drifted: %q", out)
	}
}

func TestRequireMissingExitsOne(t *testing.T) {
	t.Setenv("PLEXCTL_CONFIG_DIR", t.TempDir())
	out, code := testutil.Capture(t, func() { config.Require("token") })
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "missing config key: token — run plexctl auth login") {
		t.Fatalf("error message drifted: %q", out)
	}
}
