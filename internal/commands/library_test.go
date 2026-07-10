package commands_test

import (
	"testing"

	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/testutil"
)

func TestSearchEmptyQueryExitsUsage(t *testing.T) {
	_ = newFakePMS(t)
	root := commands.BuildRoot()
	root.SetArgs([]string{"search", "   "})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 64 {
		t.Fatalf("exit code = %d, want 64; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false || got["error"] != "query cannot be empty" {
		t.Fatalf("got %#v", got)
	}
}

func TestSearchEmptyResultsOkTrueWithNote(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search/voice", map[string]any{"MediaContainer": map[string]any{}})
	f.onJSON("GET", "/hubs/search", map[string]any{"MediaContainer": map[string]any{}})

	root := commands.BuildRoot()
	root.SetArgs([]string{"search", "zzzyyyxxx-no-match"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit code = %d, want -1 (no exit); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != true || got["note"] != "no matches" {
		t.Fatalf("got %#v", got)
	}
	results, ok := got["results"].([]any)
	if !ok || len(results) != 0 {
		t.Fatalf("results = %#v, want empty list", got["results"])
	}
}

func showHubResponse(showRatingKey, showTitle string) map[string]any {
	return map[string]any{
		"MediaContainer": map[string]any{
			"Hub": []any{
				map[string]any{"type": "show", "Metadata": []any{
					map[string]any{"ratingKey": showRatingKey, "title": showTitle, "type": "show", "score": "5"},
				}},
			},
		},
	}
}

func TestEpisodesNdjsonStreamsRowsThenSummary(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search/voice", showHubResponse("SHOW1", "Foo Show"))
	f.onJSON("GET", "/library/metadata/SHOW1/allLeaves", map[string]any{
		"MediaContainer": map[string]any{
			"Metadata": []any{
				map[string]any{"ratingKey": "1", "parentIndex": 1.0, "index": 1.0, "title": "S1E1",
					"grandparentTitle": "Foo Show", "viewCount": 0.0, "duration": 1000.0, "year": 2020.0},
				map[string]any{"ratingKey": "2", "parentIndex": 1.0, "index": 2.0, "title": "S1E2",
					"grandparentTitle": "Foo Show", "viewCount": 0.0, "duration": 1000.0, "year": 2020.0},
			},
		},
	})

	root := commands.BuildRoot()
	root.SetArgs([]string{"episodes", "Foo", "--ndjson"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1 (ndjson path never exits nonzero here); out=%s", code, out)
	}

	lines := splitNonEmpty(out)
	if len(lines) != 3 {
		t.Fatalf("want 3 lines (2 rows + summary), got %d: %v", len(lines), lines)
	}
	row1 := mustUnmarshal(t, lines[0])
	row2 := mustUnmarshal(t, lines[1])
	summary := mustUnmarshal(t, lines[2])

	if row1["ratingKey"] != "1" || row2["ratingKey"] != "2" {
		t.Fatalf("rows = %#v, %#v", row1, row2)
	}
	wantSummary := map[string]any{"ok": true, "show": "Foo Show", "showRatingKey": "SHOW1", "count": 2.0}
	for k, v := range wantSummary {
		if summary[k] != v {
			t.Fatalf("summary[%s] = %#v, want %#v (full: %#v)", k, summary[k], v, summary)
		}
	}
}

func TestEpisodesEmptyIsOkTrueWithNote(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search/voice", map[string]any{"MediaContainer": map[string]any{}})
	f.onJSON("GET", "/hubs/search", map[string]any{"MediaContainer": map[string]any{}})

	root := commands.BuildRoot()
	root.SetArgs([]string{"episodes", "Nope"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1 (ok:true, no exit); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != true || got["count"] != 0.0 {
		t.Fatalf("got %#v", got)
	}
	if _, ok := got["note"]; !ok {
		t.Fatalf("expected a note key, got %#v", got)
	}
}

// TestEpisodesNoteUsesPyReprForApostropheTitle pins jsonx.PyRepr's wiring
// into the "no episodes found for: %r" message: a query containing an
// apostrophe must render Python-repr style — double-quoted, apostrophe left
// unescaped — not Go's %q (which would double-quote and backslash-escape
// nothing relevant here, but would never single-quote or flip quote style).
func TestEpisodesNoteUsesPyReprForApostropheTitle(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search/voice", map[string]any{"MediaContainer": map[string]any{}})
	f.onJSON("GET", "/hubs/search", map[string]any{"MediaContainer": map[string]any{}})

	root := commands.BuildRoot()
	root.SetArgs([]string{"episodes", "Grey's Anatomy"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1 (ok:true, no exit); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	want := `no episodes found for: "Grey's Anatomy"`
	if got["note"] != want {
		t.Fatalf("note = %#v, want %q (full: %#v)", got["note"], want, got)
	}
}
