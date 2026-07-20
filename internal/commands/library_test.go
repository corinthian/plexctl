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
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("got %#v", got)
	}
	errBody, _ := got["error"].(map[string]any)
	if errBody["code"] != "BAD_REQUEST" || errBody["message"] != "query cannot be empty" {
		t.Fatalf("got %#v", got)
	}
}

// TestSearchMinScoreFlagRemoved pins the v2 invariant absorption: --min-score
// no longer exists as a registered flag, so cobra rejects it as an unknown
// flag (BAD_REQUEST at the root, exit 1) rather than plexctl parsing it.
func TestSearchMinScoreFlagRemoved(t *testing.T) {
	_ = newFakePMS(t)
	root := commands.BuildRoot()
	root.SetArgs([]string{"search", "Foo", "--min-score", "0.5"})
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected cobra unknown-flag error for removed --min-score, got nil")
	}
}

// TestSearchIgnoresMinScoreEnvWithWarning pins the v2 invariant absorption:
// a stale $PLEXCTL_SEARCH_MIN_SCORE in the caller's environment no longer
// pins the relevance floor (search always runs SearchTiered) — it is
// ignored, and the success envelope carries a BAD_REQUEST-coded warning
// telling the caller to remove it.
func TestSearchIgnoresMinScoreEnvWithWarning(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search", showHubResponse("100301", "The Birds"))
	t.Setenv("PLEXCTL_SEARCH_MIN_SCORE", "0.9")

	root := commands.BuildRoot()
	root.SetArgs([]string{"search", "The Birds"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1 (no exit); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != true {
		t.Fatalf("got %#v", got)
	}
	warnings, ok := got["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want one warning", got["warnings"])
	}
	w, _ := warnings[0].(map[string]any)
	if w["code"] != "BAD_REQUEST" || w["hint"] != "remove the env var" {
		t.Fatalf("warning = %#v", w)
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
					// 0.93 is what PMS actually returns for an exact title match; it
					// never emits a score above 1.0.
					map[string]any{"ratingKey": showRatingKey, "title": showTitle, "type": "show", "score": "0.93080"},
				}},
			},
		},
	}
}

func TestEpisodesNdjsonStreamsRowsThenSummary(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search", showHubResponse("SHOW1", "Foo Show"))
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

// TestMetadataNotFound pins the v2 PLEX_NOT_FOUND contract for `metadata`
// against an unknown/stale ratingKey: PMS answers with no Metadata entries.
func TestMetadataNotFound(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/library/metadata/999", map[string]any{"MediaContainer": map[string]any{}})

	root := commands.BuildRoot()
	root.SetArgs([]string{"metadata", "999"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 2 {
		t.Fatalf("exit = %d, want 2; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	errBody, _ := got["error"].(map[string]any)
	if errBody["code"] != "PLEX_NOT_FOUND" {
		t.Fatalf("got %#v", got)
	}
	data, _ := got["data"].(map[string]any)
	if data["ratingKey"] != "999" {
		t.Fatalf("data = %#v, want ratingKey 999", data)
	}
}

// TestPlayLatestAllWatchedAttachesShowIdentity pins the v2 PLEX_ALL_WATCHED
// contract for `play-latest --unwatched` against a show that resolves but
// has nothing left unwatched: code, hint, and the show/showRatingKey data
// pair (the show having already resolved by the time the error is built).
func TestPlayLatestAllWatchedAttachesShowIdentity(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search", showHubResponse("SHOW1", "Foo Show"))
	f.onJSON("GET", "/library/metadata/SHOW1/allLeaves", map[string]any{
		"MediaContainer": map[string]any{
			"Metadata": []any{
				map[string]any{"ratingKey": "1", "parentIndex": 1.0, "index": 1.0, "title": "S1E1",
					"grandparentTitle": "Foo Show", "viewCount": 1.0},
			},
		},
	})

	root := commands.BuildRoot()
	root.SetArgs([]string{"play-latest", "Foo", "--unwatched"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 2 {
		t.Fatalf("exit = %d, want 2; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	errBody, _ := got["error"].(map[string]any)
	if errBody["code"] != "PLEX_ALL_WATCHED" || errBody["hint"] != "drop --unwatched to replay, or pick another show" {
		t.Fatalf("got %#v", got)
	}
	data, _ := got["data"].(map[string]any)
	if data["show"] != "Foo Show" || data["showRatingKey"] != "SHOW1" {
		t.Fatalf("data = %#v, want show identity", data)
	}
}

// TestPlayLatestNothingFound pins the v2 PLEX_NOT_FOUND contract for
// `play-latest` when neither a show nor a movie matches the query.
func TestPlayLatestNothingFound(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search/voice", map[string]any{"MediaContainer": map[string]any{}})
	f.onJSON("GET", "/hubs/search", map[string]any{"MediaContainer": map[string]any{}})

	root := commands.BuildRoot()
	root.SetArgs([]string{"play-latest", "zzzyyyxxx-no-match"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 2 {
		t.Fatalf("exit = %d, want 2; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	errBody, _ := got["error"].(map[string]any)
	if errBody["code"] != "PLEX_NOT_FOUND" {
		t.Fatalf("got %#v", got)
	}
	data, _ := got["data"].(map[string]any)
	if data["query"] != "zzzyyyxxx-no-match" {
		t.Fatalf("data = %#v, want query", data)
	}
}
