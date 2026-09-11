package app_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/corinthian/plexctl/internal/app"
	"github.com/corinthian/plexctl/internal/config"
)

// writeConfig points PLEXCTL_CONFIG_DIR at a fresh temp dir holding body.
// internal/testutil is not used here: it imports app, and app's own test
// binary would import itself through it.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLEXCTL_CONFIG_DIR", dir)
	app.Reset()
	return dir
}

// TestTimeoutIsPerAppAndMemoised is the item-13 assertion: the resolved
// timeout is state on the invocation's App, not a package-level value in
// internal/api. One App resolves once and reads the config once; a fresh App
// resolves again, against whatever the config says then.
func TestTimeoutIsPerAppAndMemoised(t *testing.T) {
	dir := writeConfig(t, "token = \"t\"\ntimeout = 7\n")
	t.Setenv("PLEXCTL_TIMEOUT", "")

	config.ResetReads()
	a := app.New()
	if got := a.Timeout(); got != 7*time.Second {
		t.Fatalf("timeout = %v, want 7s", got)
	}
	// Memoised: the second read neither re-parses nor re-reads the file, and
	// the config the App already holds is shared with Config().
	_ = a.Config()
	if got := a.Timeout(); got != 7*time.Second {
		t.Fatalf("second read = %v, want 7s", got)
	}
	if got := config.Reads(); got != 1 {
		t.Fatalf("config read %d times for one App, want 1", got)
	}

	// A fresh App is a fresh invocation: it re-resolves.
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("token = \"t\"\ntimeout = 9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := app.New().Timeout(); got != 9*time.Second {
		t.Fatalf("a fresh App resolved %v, want 9s", got)
	}
	if got := a.Timeout(); got != 7*time.Second {
		t.Fatalf("the first App re-read the file: %v, want its memoised 7s", got)
	}
}

// TestSetTimeoutWinsOverTheConfig pins the eager half: a flag or environment
// value stored by root is the answer, and the config file is never consulted.
func TestSetTimeoutWinsOverTheConfig(t *testing.T) {
	writeConfig(t, "token = \"t\"\ntimeout = 7\n")
	t.Setenv("PLEXCTL_TIMEOUT", "")

	config.ResetReads()
	a := app.New()
	a.SetTimeout(3 * time.Second)
	if got := a.Timeout(); got != 3*time.Second {
		t.Fatalf("timeout = %v, want the stored 3s", got)
	}
	if got := config.Reads(); got != 0 {
		t.Fatalf("a stored timeout still read the config %d times", got)
	}
}

// TestTimeoutForTestSurvivesSet is the sticky-override rule. Fourteen call
// sites force a sub-second timeout and then run a command through root,
// which installs a fresh App on every invocation. The override has to
// outlive that, or every one of them silently gets the 10s default.
func TestTimeoutForTestSurvivesSet(t *testing.T) {
	writeConfig(t, "token = \"t\"\ntimeout = 7\n")
	t.Setenv("PLEXCTL_TIMEOUT", "")
	t.Cleanup(app.ClearTimeoutForTest)

	app.SetTimeoutForTest(50 * time.Millisecond)
	app.Set(app.New())
	if got := app.Current().Timeout(); got != 50*time.Millisecond {
		t.Fatalf("timeout = %v after Set, want the test override 50ms", got)
	}
	app.Reset()
	if got := app.Current().Timeout(); got != 50*time.Millisecond {
		t.Fatalf("timeout = %v after Reset, want the test override 50ms", got)
	}
	app.ClearTimeoutForTest()
	if got := app.Current().Timeout(); got != 7*time.Second {
		t.Fatalf("timeout = %v after clearing the override, want the config's 7s", got)
	}
}

// TestCurrentIsNeverNil pins the direct-package-call case: a test that never
// builds a root still gets an App that loads on demand.
func TestCurrentIsNeverNil(t *testing.T) {
	writeConfig(t, "token = \"t\"\n")
	app.Set(nil)
	if app.Current() == nil {
		t.Fatal("Current() returned nil")
	}
	if got := app.Current().Timeout(); got != app.DefaultTimeout {
		t.Fatalf("timeout = %v, want the default %v", got, app.DefaultTimeout)
	}
}
