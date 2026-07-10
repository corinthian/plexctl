// Package config ports plexctl/config.py: TOML load/save at
// ~/.config/plexctl/config.toml (or $PLEXCTL_CONFIG_DIR/config.toml — the
// same override queue_state honors; the Go port extends it to the config
// file so tests and sandboxes can redirect everything in one place).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/output"
)

// Defaults mirrors config.DEFAULTS.
var Defaults = map[string]string{
	"server_url":     "http://plex.local:32400",
	"default_client": "Apple TV",
	"client_id":      "plexctl-default",
}

// Dir returns the plexctl config directory.
func Dir() string {
	if d := os.Getenv("PLEXCTL_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".config", "plexctl")
	}
	return filepath.Join(home, ".config", "plexctl")
}

// Path returns the config.toml location.
func Path() string {
	return filepath.Join(Dir(), "config.toml")
}

// Load parses config.toml. Missing file → empty map. Malformed TOML prints
// the standard JSON error and exits 1 — a hand-edited config cannot brick
// the CLI with a stack trace.
func Load() jsonx.J {
	m, err := TryLoad()
	if err != nil {
		output.Fail(fmt.Sprintf("invalid config at %s: %v — run plexctl auth login", Path(), err))
		return jsonx.J{} // reached only when output.Exit is a test seam
	}
	return m
}

// TryLoad parses config.toml without Load's print-and-exit failure mode.
// Missing file → empty map, nil error (same as Load). Malformed TOML →
// nil map, non-nil error, instead of aborting — auth login's config-merge
// step needs to tolerate and repair a corrupt file, which Load's abort
// would defeat (running login to fix a bad config would itself abort).
func TryLoad() (jsonx.J, error) {
	b, err := os.ReadFile(Path())
	if err != nil {
		return jsonx.J{}, nil
	}
	var m map[string]any
	if err := toml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return jsonx.J{}, nil
	}
	return m, nil
}

// StringOr is Python `cfg.get(key, default)` for string-valued keys: the
// default applies only when the key is absent.
func StringOr(cfg jsonx.J, key, def string) string {
	if v, ok := cfg[key]; ok {
		return jsonx.AsStr(v)
	}
	return def
}

// Require mirrors config.require: falsy value → print the standard error and
// exit 1.
func Require(key string) string {
	v := Load()[key]
	if !jsonx.Truthy(v) {
		output.Fail(fmt.Sprintf("missing config key: %s — run plexctl auth login", key))
		return "" // test seam
	}
	return jsonx.AsStr(v)
}

// KV preserves write order — Python dicts keep insertion order, so the saved
// file's key order is part of the observable format.
type KV struct {
	K, V string
}

// Save writes key = "value" lines with the same escaping as config.save
// (backslashes and double quotes), via temp+rename like every other writer
// in this codebase (queuestate.writeAll, the commandID counter) — config.toml
// is read unlocked by every command, so a direct in-place write left a
// window where a concurrent Load could see a truncated or partial file.
func Save(pairs []KV) error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	for _, p := range pairs {
		esc := strings.ReplaceAll(p.V, `\`, `\\`)
		esc = strings.ReplaceAll(esc, `"`, `\"`)
		b.WriteString(p.K + ` = "` + esc + `"` + "\n")
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, Path()); err != nil {
		return err
	}
	// WriteFile's perm is subject to umask; chmod forces 0600 regardless,
	// matching Python's unconditional chmod(0o600).
	return os.Chmod(Path(), 0o600)
}
