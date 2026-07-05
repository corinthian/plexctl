package commands_test

import (
	"strings"
	"testing"

	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/testutil"
)

// TestChoiceErrorOnExplicitEmptyType pins choiceError's click.Choice
// behavior: an explicitly-set flag (including "" via --type=) must be
// validated against the choice list and fail as a usage error (RunE returns
// a non-nil error, which cobra propagates out of Execute() — never reaching
// output.Out, so nothing is printed and output.Exit is never called).
func TestChoiceErrorOnExplicitEmptyType(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "search --type=",
			args:    []string{"search", "x", "--type="},
			wantErr: "invalid value for '--type': '' is not one of 'show', 'movie', 'episode'",
		},
		{
			name:    "library list --type=",
			args:    []string{"library", "list", "--section", "1", "--type="},
			wantErr: "invalid value for '--type': '' is not one of 'show', 'movie'",
		},
		{
			name:    "playlist list --type=",
			args:    []string{"playlist", "list", "--type="},
			wantErr: "invalid value for '--type': '' is not one of 'video', 'audio', 'photo'",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Never expect a network call — choiceError must short-circuit
			// before any command logic runs.
			_ = newFakePMS(t)
			root := commands.BuildRoot()
			root.SetArgs(c.args)

			var err error
			out, code := testutil.Capture(t, func() { err = root.Execute() })

			if err == nil {
				t.Fatalf("args %v: expected a usage error, got nil", c.args)
			}
			if err.Error() != c.wantErr {
				t.Errorf("args %v: err = %q, want %q", c.args, err.Error(), c.wantErr)
			}
			if code != -1 {
				t.Errorf("args %v: exit = %d, want -1 (usage error never reaches output.Exit)", c.args, code)
			}
			if out != "" {
				t.Errorf("args %v: expected no stdout JSON, got %q", c.args, out)
			}
		})
	}
}

// TestPlaylistListNoTypeRunsNormally is the choiceError control case: an
// unset --type (the common case) must keep the default and run the command
// through to completion instead of failing validation.
func TestPlaylistListNoTypeRunsNormally(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/playlists", map[string]any{"MediaContainer": map[string]any{}})

	root := commands.BuildRoot()
	root.SetArgs([]string{"playlist", "list"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1 (ok:true, no exit); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != true || got["count"] != 0.0 {
		t.Fatalf("got %#v", got)
	}
}

// TestSearchExplicitValidTypePassesChoiceValidation confirms a valid,
// explicitly-set --type sails through choiceError and the command completes
// normally.
func TestSearchExplicitValidTypePassesChoiceValidation(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search/voice", map[string]any{"MediaContainer": map[string]any{}})
	f.onJSON("GET", "/hubs/search", map[string]any{"MediaContainer": map[string]any{}})

	root := commands.BuildRoot()
	root.SetArgs([]string{"search", "x", "--type", "movie"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1 (ok:true, no exit); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != true {
		t.Fatalf("got %#v", got)
	}
	if !strings.Contains(out, `"note":"no matches"`) {
		t.Fatalf("out = %s, want a no-matches note", out)
	}
}
