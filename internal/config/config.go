// Package config ports plexctl/config.py: TOML load/save at
// ~/.config/plexctl/config.toml (or $PLEXCTL_CONFIG_DIR/config.toml — the
// same override queue_state honors; the Go port extends it to the config
// file so tests and sandboxes can redirect everything in one place).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
		output.FailErr(output.Err(output.CodeAuthRequired,
			fmt.Sprintf("invalid config at %s: %v — run plexctl auth login", Path(), err)).
			WithHint("run: plexctl auth login"))
		return jsonx.J{} // reached only when output.Exit is a test seam
	}
	return m
}

// TryLoad parses config.toml without Load's print-and-exit failure mode.
// Missing file → empty map, nil error (same as Load). Malformed TOML →
// nil map, non-nil error, instead of aborting — auth login's config-merge
// step needs to tolerate and repair a corrupt file, which Load's abort
// would defeat (running login to fix a bad config would itself abort).
//
// Only os.ErrNotExist is "absent". Every other read error — a permissions
// or I/O failure on a file that may be perfectly valid — is returned, not
// flattened to an empty map: login merges onto TryLoad's result and saves
// through a rename, so "unreadable" reported as "absent" silently destroys
// every unmanaged key the file held.
func TryLoad() (jsonx.J, error) {
	b, err := os.ReadFile(Path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return jsonx.J{}, nil
		}
		return nil, err
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
		output.FailErr(output.Err(output.CodeAuthRequired,
			fmt.Sprintf("missing config key: %s — run plexctl auth login", key)).
			WithHint("run: plexctl auth login"))
		return "" // test seam
	}
	return jsonx.AsStr(v)
}

// KV is one config key and its value. V is `any` so a value keeps the TOML
// type it was loaded with: a numeric `timeout = 10` that round-trips through
// login stays an integer instead of being restringed.
type KV struct {
	K string
	V any
}

// Save encodes the pairs with the TOML marshaller rather than formatting
// `key = "value"` lines by hand. The hand-rolled writer escaped backslashes
// and double quotes only, so any other TOML-significant byte in a value —
// a newline in a client name being the reachable case — produced a file the
// CLI could no longer parse, bricking every later command until the user
// hand-repaired it.
//
// Key order is now the encoder's, not the caller's. Nothing but plexctl
// reads config.toml, so order is cosmetic; the encoder writes scalars ahead
// of sub-tables, which is what TOML validity requires anyway.
//
// The write is temp+rename like every other writer in this codebase
// (queuestate.writeAll, the commandID counter) — config.toml is read
// unlocked by every command, so a direct in-place write left a window where
// a concurrent Load could see a truncated or partial file.
func Save(pairs []KV) error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return err
	}
	// MkdirAll won't tighten a pre-existing directory's mode, and Save is
	// the only token-writing path — this is the one place that needs to
	// cover the upgrade case from an older, world-readable config dir.
	_ = os.Chmod(Dir(), 0o700)
	doc := make(map[string]any, len(pairs))
	for _, p := range pairs {
		doc[p.K] = p.V
	}
	encoded, err := toml.Marshal(doc)
	if err != nil {
		return err
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, Path()); err != nil {
		return err
	}
	// WriteFile's perm is subject to umask; chmod forces 0600 regardless,
	// matching Python's unconditional chmod(0o600).
	return os.Chmod(Path(), 0o600)
}
