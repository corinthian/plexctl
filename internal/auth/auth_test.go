package auth

import (
	"os"
	"reflect"
	"testing"

	"github.com/corinthian/plexctl/internal/config"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/output"
)

// TestMergeConfigPairsPreservesHandAddedKey pins W5: auth login used to
// Save only its own four keys, silently destroying any other key the
// config already had — the README-documented `timeout` included.
func TestMergeConfigPairsPreservesHandAddedKey(t *testing.T) {
	existing := jsonx.J{"timeout": int64(10)}
	pairs := mergeConfigPairs(existing, "http://pms:32400", "tok", "Apple TV", "cid-1")

	want := []config.KV{
		{K: "timeout", V: "10"},
		{K: "server_url", V: "http://pms:32400"},
		{K: "token", V: "tok"},
		{K: "default_client", V: "Apple TV"},
		{K: "client_id", V: "cid-1"},
	}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("pairs = %#v, want %#v", pairs, want)
	}
}

// TestMergeConfigPairsManagedKeysAlwaysOverwritten proves the four
// auth-managed keys always take the freshly authenticated values, even
// when the existing config already had (stale) values for them.
func TestMergeConfigPairsManagedKeysAlwaysOverwritten(t *testing.T) {
	existing := jsonx.J{
		"server_url":     "http://stale:32400",
		"token":          "stale-token",
		"default_client": "Old Client",
		"client_id":      "stale-cid",
		"timeout":        "30",
	}
	pairs := mergeConfigPairs(existing, "http://fresh:32400", "fresh-token", "Apple TV", "fresh-cid")

	want := []config.KV{
		{K: "timeout", V: "30"},
		{K: "server_url", V: "http://fresh:32400"},
		{K: "token", V: "fresh-token"},
		{K: "default_client", V: "Apple TV"},
		{K: "client_id", V: "fresh-cid"},
	}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("pairs = %#v, want %#v", pairs, want)
	}
}

// TestMergeConfigPairsEmptyExisting covers first-ever login: no prior
// config, only the four managed keys are written.
func TestMergeConfigPairsEmptyExisting(t *testing.T) {
	pairs := mergeConfigPairs(jsonx.J{}, "http://pms:32400", "tok", "Apple TV", "cid-1")

	want := []config.KV{
		{K: "server_url", V: "http://pms:32400"},
		{K: "token", V: "tok"},
		{K: "default_client", V: "Apple TV"},
		{K: "client_id", V: "cid-1"},
	}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("pairs = %#v, want %#v", pairs, want)
	}
}

// TestValidatePMSURL pins W5 (finding 2, salvaged parts): reject any scheme
// other than http/https, userinfo, fragments, and query strings before the
// URL is ever used on the network.
func TestValidatePMSURL(t *testing.T) {
	rejects := []string{
		"ftp://pms.example:32400",             // wrong scheme
		"http://user:pass@pms.example:32400",  // userinfo
		"http:///just/a/path",                 // hostless
		"http://pms.example:32400#fragment",   // fragment
		"http://pms.example:32400?token=leak", // query string
		"not a url at all",                    // unparseable / no scheme
	}
	for _, raw := range rejects {
		t.Run("reject "+raw, func(t *testing.T) {
			if _, err := validatePMSURL(raw); err == nil {
				t.Fatalf("validatePMSURL(%q) = nil error, want a rejection", raw)
			}
		})
	}

	accepts := []string{
		"http://pms.example:32400",
		"https://pms.example:32400",
		"http://pms.example:32400/",
		"https://10.0.0.5:32400",
	}
	for _, raw := range accepts {
		t.Run("accept "+raw, func(t *testing.T) {
			if _, err := validatePMSURL(raw); err != nil {
				t.Fatalf("validatePMSURL(%q) = %v, want no error", raw, err)
			}
		})
	}
}

// TestLoadOrQuarantineCleanConfig covers the two non-destructive paths of
// login's single config read: a readable file hands its keys back untouched
// with no backup, and an absent file (first-ever login) is an empty map,
// not an error.
func TestLoadOrQuarantineCleanConfig(t *testing.T) {
	t.Run("readable file", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("PLEXCTL_CONFIG_DIR", dir)
		if err := os.WriteFile(config.Path(), []byte("timeout = 10\ntoken = \"old\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		existing, backup, cliErr := loadOrQuarantineConfig()
		if cliErr != nil {
			t.Fatalf("cliErr = %#v, want nil", cliErr)
		}
		if backup != "" {
			t.Fatalf("backup = %q, want empty (nothing was quarantined)", backup)
		}
		if existing["timeout"] != int64(10) || existing["token"] != "old" {
			t.Fatalf("existing = %#v", existing)
		}
		if _, err := os.Stat(config.Path()); err != nil {
			t.Fatalf("config.toml must survive a clean read: %v", err)
		}
	})

	t.Run("absent file", func(t *testing.T) {
		t.Setenv("PLEXCTL_CONFIG_DIR", t.TempDir())
		existing, backup, cliErr := loadOrQuarantineConfig()
		if cliErr != nil || backup != "" || len(existing) != 0 {
			t.Fatalf("existing=%#v backup=%q cliErr=%#v, want empty/empty/nil", existing, backup, cliErr)
		}
	})
}

// TestQuarantineCorruptConfig pins the destructive path. An unusable config
// can't have its unmanaged keys preserved through the rename-over save, so
// login makes the loss explicit instead of silent: the original bytes move
// to config.toml.corrupt-<RFC3339> and login continues with an empty map.
// "Unusable" covers a non-ENOENT read failure as well as a parse failure —
// TryLoad used to report a permissions error as a clean absent file, which
// is what let the save destroy a perfectly good config nobody could read.
func TestQuarantineCorruptConfig(t *testing.T) {
	readBackup := func(t *testing.T, backup string) string {
		t.Helper()
		if backup == "" {
			t.Fatal("backup path is empty, want the quarantined file")
		}
		if _, err := os.Stat(config.Path()); !os.IsNotExist(err) {
			t.Fatalf("config.toml still present after quarantine: err=%v", err)
		}
		if err := os.Chmod(backup, 0o600); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(backup)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	t.Run("malformed TOML", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("PLEXCTL_CONFIG_DIR", dir)
		const original = "not = = toml\n"
		if err := os.WriteFile(config.Path(), []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		existing, backup, cliErr := loadOrQuarantineConfig()
		if cliErr != nil {
			t.Fatalf("cliErr = %#v, want nil (login must repair, not abort)", cliErr)
		}
		if len(existing) != 0 {
			t.Fatalf("existing = %#v, want an empty map", existing)
		}
		if got := readBackup(t, backup); got != original {
			t.Fatalf("backup holds %q, want the original bytes %q", got, original)
		}
	})

	t.Run("unreadable file", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores file modes")
		}
		dir := t.TempDir()
		t.Setenv("PLEXCTL_CONFIG_DIR", dir)
		const original = "token = \"still-good\"\n"
		if err := os.WriteFile(config.Path(), []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(config.Path(), 0o000); err != nil {
			t.Fatal(err)
		}
		existing, backup, cliErr := loadOrQuarantineConfig()
		if cliErr != nil {
			t.Fatalf("cliErr = %#v, want nil", cliErr)
		}
		if len(existing) != 0 {
			t.Fatalf("existing = %#v, want an empty map", existing)
		}
		if got := readBackup(t, backup); got != original {
			t.Fatalf("backup holds %q, want the original bytes %q", got, original)
		}
	})

	t.Run("rename failure is INTERNAL, not destruction", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory modes")
		}
		dir := t.TempDir()
		t.Setenv("PLEXCTL_CONFIG_DIR", dir)
		if err := os.WriteFile(config.Path(), []byte("not = = toml\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		// A read-execute directory still allows reading the file but not
		// creating the backup name in it.
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		_, backup, cliErr := loadOrQuarantineConfig()
		if cliErr == nil {
			t.Fatal("cliErr = nil, want INTERNAL rather than proceeding with the file left behind")
		}
		if cliErr.Code != output.CodeInternal || cliErr.ExitCode() != 4 {
			t.Fatalf("code=%q exit=%d, want %q/4", cliErr.Code, cliErr.ExitCode(), output.CodeInternal)
		}
		if backup != "" {
			t.Fatalf("backup = %q, want empty on failure", backup)
		}
		if _, err := os.Stat(config.Path()); err != nil {
			t.Fatalf("config.toml must be left intact when it cannot be moved aside: %v", err)
		}
	})
}
