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
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out=%s", code, out)
	}
	want := `{"error":"nothing playing — provide a ratingKey","ok":false}`
	if got := trimNL(out); got != want {
		t.Fatalf("out = %q, want %q", got, want)
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
