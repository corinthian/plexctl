package commands_test

import (
	"net/http"
	"testing"

	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/testutil"
)

// --- seek's hand-rolled parser ------------------------------------------
//
// newSeekCmd disables cobra's flag parsing entirely (DisableFlagParsing) so
// that flag-like tokens such as "-30s" survive as POSITION, mirroring
// click's ignore_unknown_options + allow_extra_args on this one command.
// That means: cobra never intercepts --help/-h for this command (ParseFlags
// is a no-op when DisableFlagParsing is set — see cobra's Command.ParseFlags
// and Command.execute), and only the hand-parsed -c/--client/--no-unpause/
// --/--help tokens are recognized; everything else — including --timeout,
// which cobra normally intercepts as a root persistent flag, and -h — joins
// POSITION verbatim. Client resolution happens before position parsing
// (clients.Resolve is evaluated as playback.Seek's first argument), so every
// case here needs the fake /clients + /devices.json wiring even when the
// position itself is malformed.

func TestSeekUnrecognisedPositionSwallowsTimeoutFlag(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)

	root := commands.BuildRoot()
	root.SetArgs([]string{"seek", "1:30", "--timeout", "5"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out=%s", code, out)
	}
	want := `{"error":"unrecognised position format: '1:30 --timeout 5'","ok":false}`
	if got := trimNL(out); got != want {
		t.Fatalf("out = %q, want %q", got, want)
	}
}

func TestSeekBareDashHJoinsPositionInsteadOfTriggeringHelp(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)

	root := commands.BuildRoot()
	root.SetArgs([]string{"seek", "1:30", "-h"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (not help output); out=%s", code, out)
	}
	want := `{"error":"unrecognised position format: '1:30 -h'","ok":false}`
	if got := trimNL(out); got != want {
		t.Fatalf("out = %q, want %q", got, want)
	}
}

func TestSeekDashDashEndsFlagRecognitionButTokenJoinsPosition(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)

	root := commands.BuildRoot()
	root.SetArgs([]string{"seek", "1:30", "--", "--no-unpause"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out=%s", code, out)
	}
	want := `{"error":"unrecognised position format: '1:30 --no-unpause'","ok":false}`
	if got := trimNL(out); got != want {
		t.Fatalf("out = %q, want %q", got, want)
	}
}

// playingSession wires /status/sessions with a single session for the
// resolvableClient's "Apple TV" (machineIdentifier "mid-appletv") at the
// given viewOffset (ms) and player state, so playback.Seek's relative-seek
// math and pause/resume dance have something to read.
func playingSession(f *fakePMS, viewOffsetMs float64, state string) {
	f.onJSON("GET", "/status/sessions", map[string]any{
		"MediaContainer": map[string]any{
			"Metadata": []any{
				map[string]any{
					"viewOffset": viewOffsetMs,
					"Player":     map[string]any{"machineIdentifier": "mid-appletv", "state": state},
				},
			},
		},
	})
}

func TestSeekRelativeNegativeParsesFlagLikeTokenAsPosition(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	playingSession(f, 60000, "playing")

	var gotOffset string
	f.on("GET", "/player/playback/seekTo", func(r *http.Request) (int, any) {
		gotOffset = r.URL.Query().Get("offset")
		return 200, map[string]any{}
	})

	root := commands.BuildRoot()
	root.SetArgs([]string{"seek", "-30s"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1 (successful seek); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != true {
		t.Fatalf("expected ok:true (position token reached playback.Seek), got %#v (out=%s)", got, out)
	}
	if gotOffset != "30000" {
		t.Fatalf("seekTo offset = %q, want %q (60000ms - 30000ms)", gotOffset, "30000")
	}
}

func TestSeekClientFlagConsumedNotJoinedIntoPosition(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	playingSession(f, 60000, "playing")

	var gotOffset string
	f.on("GET", "/player/playback/seekTo", func(r *http.Request) (int, any) {
		gotOffset = r.URL.Query().Get("offset")
		return 200, map[string]any{}
	})

	root := commands.BuildRoot()
	root.SetArgs([]string{"seek", "--client", "Apple TV", "+30s"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1 (successful seek); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != true {
		t.Fatalf("expected ok:true (--client consumed, position is '+30s'), got %#v (out=%s)", got, out)
	}
	if gotOffset != "90000" {
		t.Fatalf("seekTo offset = %q, want %q (60000ms + 30000ms)", gotOffset, "90000")
	}
}

func TestSeekNoArgsIsUsageError(t *testing.T) {
	_ = newFakePMS(t)
	root := commands.BuildRoot()
	root.SetArgs([]string{"seek"})

	var err error
	out, code := testutil.Capture(t, func() { err = root.Execute() })
	if err == nil {
		t.Fatalf("expected a usage error for missing POSITION, got nil")
	}
	if err.Error() != "POSITION required" {
		t.Errorf("err = %q, want %q", err.Error(), "POSITION required")
	}
	if code != -1 {
		t.Errorf("exit = %d, want -1 (usage error never reaches output.Exit)", code)
	}
	if out != "" {
		t.Errorf("expected no stdout JSON, got %q", out)
	}
}
