package commands_test

import (
	"testing"

	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/testutil"
)

func TestWatchedNothingPlaying(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/status/sessions", map[string]any{"MediaContainer": map[string]any{}})

	root := commands.BuildRoot()
	root.SetArgs([]string{"watched"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 2 {
		t.Fatalf("exit = %d, want 2; out=%s", code, out)
	}
	want := `{"error":{"code":"PLEX_NOTHING_PLAYING","hint":"provide a ratingKey","message":"nothing playing — provide a ratingKey"},"ok":false}`
	if got := trimNL(out); got != want {
		t.Fatalf("out = %q, want %q", got, want)
	}
}

// TestUnwatchedNothingPlaying and TestRateNothingPlaying pin the same
// PLEX_NOTHING_PLAYING contract on unwatched/rate's identical guarded-idle
// site (watch.go's other two copies of the same resolveTargetKey check).
func TestUnwatchedNothingPlaying(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/status/sessions", map[string]any{"MediaContainer": map[string]any{}})

	root := commands.BuildRoot()
	root.SetArgs([]string{"unwatched"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 2 {
		t.Fatalf("exit = %d, want 2; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	errBody, _ := got["error"].(map[string]any)
	if errBody["code"] != "PLEX_NOTHING_PLAYING" || errBody["hint"] != "provide a ratingKey" {
		t.Fatalf("got %#v", got)
	}
}

func TestRateNothingPlaying(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/status/sessions", map[string]any{"MediaContainer": map[string]any{}})

	root := commands.BuildRoot()
	root.SetArgs([]string{"rate", "5"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 2 {
		t.Fatalf("exit = %d, want 2; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	errBody, _ := got["error"].(map[string]any)
	if errBody["code"] != "PLEX_NOTHING_PLAYING" || errBody["hint"] != "provide a ratingKey" {
		t.Fatalf("got %#v", got)
	}
}

func TestRateRangeValidation(t *testing.T) {
	_ = newFakePMS(t)
	root := commands.BuildRoot()
	root.SetArgs([]string{"rate", "11"})
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected a usage error for RATING out of range, got nil")
	}
}

// TestWatchExplicitKeyNoClientResolve pins the item-6 fix. watched /
// unwatched / rate resolved the target client before reading the positional
// ratingKey, so marking an item watched by key needed the Apple TV awake and
// registered — an explicit key failed PLEX_CLIENT_UNKNOWN / _INACTIVE (exit
// 2) or CLOUD_UNREACHABLE (exit 3) for a call that needs no client at all.
//
// This is a negative test: /clients and /devices.json answer HTTP 500, so
// any discovery call fails the command. Asserting the exit alone would not
// distinguish "never asked" from "asked and got away with it", so it also
// asserts the request log. plex.tv is redirected onto the fake even though
// nothing should reach it — without that, a regression here would hit the
// real plex.tv and still go green.
func TestWatchExplicitKeyNoClientResolve(t *testing.T) {
	cases := []struct {
		name string
		args []string
		path string
	}{
		{"watched", []string{"watched", "123"}, "/:/scrobble"},
		{"unwatched", []string{"unwatched", "123"}, "/:/unscrobble"},
		{"rate", []string{"rate", "7", "123"}, "/:/rate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakePMS(t)
			f.redirectPlexTV(t)
			f.onStatus("GET", "/clients", 500)
			f.onStatus("GET", "/devices.json", 500)
			f.onStatus("GET", "/status/sessions", 500)
			f.onJSON("GET", tc.path, map[string]any{})

			root := commands.BuildRoot()
			root.SetArgs(tc.args)
			out, code := testutil.Capture(t, func() { _ = root.Execute() })
			if code != -1 {
				t.Fatalf("exit = %d, want no exit (success); out=%s", code, out)
			}
			if got := mustUnmarshal(t, out); got["ok"] != true {
				t.Fatalf("out = %s, want an ok:true envelope", out)
			}
			if n := f.countPath(tc.path); n != 1 {
				t.Fatalf("%s hit %d times, want 1", tc.path, n)
			}
			for _, discovery := range []string{"/clients", "/devices.json", "/status/sessions"} {
				if n := f.countPath(discovery); n != 0 {
					t.Fatalf("%s hit %d times — an explicit ratingKey must not trigger discovery", discovery, n)
				}
			}
		})
	}
}

// TestWatchedExplicitKeyIgnoresClientFlag pins the deliberate leniency:
// --client alongside an explicit ratingKey is inert, not rejected. Rejecting
// would break habitual callers that always pass it, for no safety gain —
// there is no client involved in scrobbling a known key.
func TestWatchedExplicitKeyIgnoresClientFlag(t *testing.T) {
	f := newFakePMS(t)
	f.redirectPlexTV(t)
	f.onStatus("GET", "/clients", 500)
	f.onStatus("GET", "/devices.json", 500)
	f.onJSON("GET", "/:/scrobble", map[string]any{})

	root := commands.BuildRoot()
	root.SetArgs([]string{"watched", "123", "--client", "Nonexistent Device"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want no exit; out=%s", code, out)
	}
	if n := f.countPath("/:/scrobble"); n != 1 {
		t.Fatalf("scrobble hit %d times, want 1", n)
	}
}
