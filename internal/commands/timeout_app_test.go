package commands_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/config"
)

// setConfigTimeout appends a timeout key to the config the fake PMS wrote,
// keeping server_url and token pointed at the fake.
func setConfigTimeout(t *testing.T, dir, serverURL, line string) {
	t.Helper()
	body := "server_url = \"" + serverURL + "\"\n" +
		"token = \"test-token\"\ndefault_client = \"Apple TV\"\nclient_id = \"plexctl-test\"\n" + line + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTimeoutResolvedOncePerInvocation is the second half of item 12's
// read-once assertion: the timeout's config candidate shares the
// invocation's one memoised load rather than reading the file for itself.
// The command resolves a timeout and then makes several requests; the file
// is read once.
func TestTimeoutResolvedOncePerInvocation(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/status/sessions", map[string]any{"MediaContainer": map[string]any{}})
	setConfigTimeout(t, f.dir, f.srv.URL, "timeout = 5")
	t.Setenv("PLEXCTL_TIMEOUT", "")
	t.Cleanup(api.ClearTimeoutForTest)

	config.ResetReads()
	out, code := runRoot(t, "now-playing")
	if code != -1 {
		t.Fatalf("now-playing exited %d; out=%s", code, out)
	}
	if got := config.Reads(); got != 1 {
		t.Fatalf("config read %d times, want 1; out=%s", got, out)
	}
	if got := api.Timeout(); got != 5*time.Second {
		t.Fatalf("resolved timeout = %v, want the config's 5s", got)
	}
}

// TestSeekResolvesThroughTheSameApp pins contract 2.1's plexctl exception
// from the other side. seek's DisableFlagParsing keeps root's persistent
// flag from ever being marked Changed, so seek resolves its own --timeout;
// when it supplies none, root's resolution is the value it falls back to,
// and both read the same sources through the same App.
func TestSeekResolvesThroughTheSameApp(t *testing.T) {
	t.Run("no seek flag falls back to root's resolution", func(t *testing.T) {
		f := newFakePMS(t)
		f.resolvableClient(t)
		playingSession(f, 90000, "playing")
		f.onJSON("GET", "/player/playback/seekTo", map[string]any{})
		setConfigTimeout(t, f.dir, f.srv.URL, "timeout = 7")
		t.Setenv("PLEXCTL_TIMEOUT", "")
		t.Cleanup(api.ClearTimeoutForTest)

		config.ResetReads()
		out, code := runRoot(t, "seek", "1:30")
		if code != -1 {
			t.Fatalf("seek exited %d; out=%s", code, out)
		}
		if got := api.Timeout(); got != 7*time.Second {
			t.Fatalf("seek resolved %v, want the config's 7s", got)
		}
		if got := config.Reads(); got != 1 {
			t.Fatalf("config read %d times during seek, want 1", got)
		}
	})

	t.Run("seek's own flag outranks the config, as root's would", func(t *testing.T) {
		f := newFakePMS(t)
		f.resolvableClient(t)
		playingSession(f, 90000, "playing")
		f.onJSON("GET", "/player/playback/seekTo", map[string]any{})
		setConfigTimeout(t, f.dir, f.srv.URL, "timeout = 7")
		t.Setenv("PLEXCTL_TIMEOUT", "")
		t.Cleanup(api.ClearTimeoutForTest)

		out, code := runRoot(t, "seek", "--timeout", "9", "1:30")
		if code != -1 {
			t.Fatalf("seek exited %d; out=%s", code, out)
		}
		if got := api.Timeout(); got != 9*time.Second {
			t.Fatalf("seek --timeout 9 resolved %v, want 9s", got)
		}
	})
}
