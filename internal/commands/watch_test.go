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
