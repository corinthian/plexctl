package commands_test

import (
	"strings"
	"testing"

	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/testutil"
)

// errBody unpacks the v2 structured error envelope's "error" object:
// {"ok": false, "error": {"code", "message", "hint"?}, "data"?}.
func errBody(t *testing.T, got map[string]any) map[string]any {
	t.Helper()
	body, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("error field = %#v, want a structured {code,message} object", got["error"])
	}
	return body
}

// TestSetAudioSetSubtitleGuardStrings covers every mutually-exclusive-flag
// guard, v2 BAD_REQUEST envelope, in cli.py's exact wording (message stays
// human-readable free text under the new "error.message" key). Exit stays 1
// (BAD_REQUEST's class). None of these paths touch the network — the guard
// fires before any domain call.
func TestSetAudioSetSubtitleGuardStrings(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			"neither-target",
			[]string{"set-audio", "--language", "eng"},
			"provide RATING_KEY (single) or --show/--show-rating-key (bulk)",
		},
		{
			"both-ratingkey-and-show",
			[]string{"set-audio", "123", "--show", "Foo"},
			"provide RATING_KEY (single) or --show/--show-rating-key (bulk), not both",
		},
		{
			"show-and-show-rating-key-exclusive",
			[]string{"set-audio", "--show", "Foo", "--show-rating-key", "9"},
			"--show and --show-rating-key are mutually exclusive",
		},
		{
			"stream-id-invalid-in-bulk",
			[]string{"set-audio", "--show", "Foo", "--stream-id", "5"},
			"--stream-id is single-item only; not valid with --show",
		},
		{
			"subtitle-off-and-language-exclusive",
			[]string{"set-subtitle", "123", "--off", "--language", "eng"},
			"--off and --language/--stream-id are mutually exclusive",
		},
		{
			"subtitle-language-and-stream-id-exclusive",
			[]string{"set-subtitle", "123", "--language", "eng", "--stream-id", "5"},
			"--language and --stream-id are mutually exclusive",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = newFakePMS(t)
			root := commands.BuildRoot()
			root.SetArgs(tc.args)
			out, code := testutil.Capture(t, func() { _ = root.Execute() })
			if code != 1 {
				t.Fatalf("exit = %d, want 1; out=%s", code, out)
			}
			got := mustUnmarshal(t, out)
			if got["ok"] != false {
				t.Fatalf("ok = %#v, want false", got["ok"])
			}
			body := errBody(t, got)
			if body["code"] != output.CodeBadRequest {
				t.Fatalf("code = %#v, want %q", body["code"], output.CodeBadRequest)
			}
			if body["message"] != tc.want {
				t.Fatalf("message = %#v, want %q", body["message"], tc.want)
			}
		})
	}
}

// TestSetAudioSingleItemLanguageStreamIDGuardBadRequest — the seventh
// flag-validation site (set-audio single-item), migrated in the post-P2
// sweep: the inventory's "six" was an undercount, this guard is
// message-identical to the set-subtitle sibling and codes the same way.
func TestSetAudioSingleItemLanguageStreamIDGuardBadRequest(t *testing.T) {
	_ = newFakePMS(t)
	root := commands.BuildRoot()
	root.SetArgs([]string{"set-audio", "123", "--language", "eng", "--stream-id", "5"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	errObj, _ := got["error"].(map[string]any)
	if got["ok"] != false || errObj == nil || errObj["code"] != "BAD_REQUEST" ||
		errObj["message"] != "--language and --stream-id are mutually exclusive" {
		t.Fatalf("got %#v, want BAD_REQUEST envelope", got)
	}
}

func TestBulkSetAudioAmbiguousShowRefusesNoWrites(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search", map[string]any{
		"MediaContainer": map[string]any{
			"Hub": []any{
				map[string]any{"type": "show", "Metadata": []any{
					map[string]any{"ratingKey": "1", "title": "Tom & Jerry", "type": "show", "score": "0.93084"},
					map[string]any{"ratingKey": "2", "title": "Tom & Jerry Kids", "type": "show", "score": "0.93071"},
				}},
			},
		},
	})

	root := commands.BuildRoot()
	root.SetArgs([]string{"set-audio", "--show", "Tom and Jerry", "--language", "eng"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (ExitPlex); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false", got["ok"])
	}
	body := errBody(t, got)
	if body["code"] != output.CodeShowAmbiguous {
		t.Fatalf("code = %#v, want %q", body["code"], output.CodeShowAmbiguous)
	}
	if want := "ambiguous show 'Tom and Jerry' — 2 series match; pass --show-rating-key"; body["message"] != want {
		t.Fatalf("message = %#v, want %q", body["message"], want)
	}
	if want := "disambiguate with --show-rating-key KEY"; body["hint"] != want {
		t.Fatalf("hint = %#v, want %q", body["hint"], want)
	}
	data, _ := got["data"].(map[string]any)
	matches, _ := data["matches"].([]any)
	if len(matches) != 2 {
		t.Fatalf("data.matches = %#v, want 2 entries", data["matches"])
	}
	if n := f.countMethod("PUT"); n != 0 {
		t.Fatalf("PUT calls = %d, want 0 (ambiguity must refuse before any write)", n)
	}
}

// The bulk resolver feeds a write. It counts distinct ratingKeys and refuses on
// more than one, so anything that inflates the hit set turns a clean resolve into
// a spurious "ambiguous show" refusal. The tiered search widens into the weak
// band when nothing scores confidently — this pins that the token guard keeps
// noise out of that count, so a real show still resolves to exactly one hit.
func TestBulkSetAudioResolverIsNotInflatedByWeakBandNoise(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search", map[string]any{
		"MediaContainer": map[string]any{
			"Hub": []any{
				map[string]any{"type": "show", "Metadata": []any{
					map[string]any{"ratingKey": "1440", "title": "Black Books", "type": "show", "score": "0.93071"},
					// Weak-band noise sharing no token with the query. It must not
					// reach the ambiguity guard.
					map[string]any{"ratingKey": "999", "title": "His Dark Materials", "type": "show", "score": "0.33078"},
				}},
			},
		},
	})
	f.onJSON("GET", "/library/metadata/1440/allLeaves", map[string]any{
		"MediaContainer": map[string]any{
			"Metadata": []any{
				map[string]any{"ratingKey": "10", "parentIndex": 1.0, "index": 1.0, "title": "S1E1"},
			},
		},
	})
	f.onJSON("GET", "/library/metadata/10", map[string]any{
		"MediaContainer": map[string]any{
			"Metadata": []any{
				map[string]any{"ratingKey": "10", "Media": []any{
					map[string]any{"Part": []any{
						map[string]any{"id": 500.0, "Stream": []any{
							map[string]any{"id": 1.0, "streamType": 2.0, "languageCode": "eng", "language": "English"},
						}},
					}},
				}},
			},
		},
	})

	root := commands.BuildRoot()
	root.SetArgs([]string{"set-audio", "--show", "Black Books", "--language", "eng", "--dry-run"})
	out, _ := testutil.Capture(t, func() { _ = root.Execute() })

	got := mustUnmarshal(t, out)
	if errStr, _ := got["error"].(string); strings.Contains(errStr, "ambiguous") {
		t.Fatalf("resolver refused as ambiguous: %q — weak-band noise must not reach the distinct count", errStr)
	}
	if got["ok"] != true {
		t.Fatalf("got %#v, want a clean resolve to Black Books", got)
	}
}

func TestBulkSetAudioNoShowFoundRefuses(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search/voice", map[string]any{"MediaContainer": map[string]any{}})
	f.onJSON("GET", "/hubs/search", map[string]any{"MediaContainer": map[string]any{}})

	root := commands.BuildRoot()
	root.SetArgs([]string{"set-audio", "--show", "Nope", "--language", "eng"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (ExitPlex); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false", got["ok"])
	}
	body := errBody(t, got)
	if body["code"] != output.CodeNotFound {
		t.Fatalf("code = %#v, want %q", body["code"], output.CodeNotFound)
	}
	if want := "no show found for: 'Nope' — pass --show-rating-key"; body["message"] != want {
		t.Fatalf("message = %#v, want %q", body["message"], want)
	}
	if n := f.countMethod("PUT"); n != 0 {
		t.Fatalf("PUT calls = %d, want 0", n)
	}
}

func TestBulkSetAudioMultiSeasonWithoutAllSeasonsRefused(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search", showHubResponse("1", "Show"))
	f.onJSON("GET", "/library/metadata/1/allLeaves", map[string]any{
		"MediaContainer": map[string]any{
			"Metadata": []any{
				map[string]any{"ratingKey": "10", "parentIndex": 4.0, "index": 1.0, "title": "S4E1"},
				map[string]any{"ratingKey": "11", "parentIndex": 5.0, "index": 1.0, "title": "S5E1"},
			},
		},
	})

	root := commands.BuildRoot()
	root.SetArgs([]string{"set-audio", "--show", "Show", "--language", "eng"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (ExitPlex); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false", got["ok"])
	}
	body := errBody(t, got)
	if body["code"] != output.CodeScopeRequired {
		t.Fatalf("code = %#v, want %q", body["code"], output.CodeScopeRequired)
	}
	want := "'Show' spans 2 seasons [4, 5]; pass --season N or --all-seasons"
	if body["message"] != want {
		t.Fatalf("message = %#v, want %q", body["message"], want)
	}
	if want := "add --all-seasons, or narrow with --season N"; body["hint"] != want {
		t.Fatalf("hint = %#v, want %q", body["hint"], want)
	}
	data, _ := got["data"].(map[string]any)
	seasons, _ := data["seasons"].(map[string]any)
	if seasons["4"] != 1.0 || seasons["5"] != 1.0 {
		t.Fatalf("data.seasons = %#v, want {4:1, 5:1}", data["seasons"])
	}
	if n := f.countMethod("PUT"); n != 0 {
		t.Fatalf("PUT calls = %d, want 0 (scope guard must refuse before any write)", n)
	}
}

// The season-scope guard gates a bulk write: more than one season and it refuses
// unless --season or --all-seasons is given. An unparented episode coerced to 0
// invents a Specials season, so a genuinely single-season show looks like two and
// the write is refused for a reason that does not exist.
func TestBulkSetAudioSeasonGuardIgnoresUnparentedEpisodes(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search/voice", showHubResponse("1", "Show"))
	f.onJSON("GET", "/library/metadata/1/allLeaves", map[string]any{
		"MediaContainer": map[string]any{
			"Metadata": []any{
				map[string]any{"ratingKey": "10", "parentIndex": 4.0, "index": 1.0, "title": "S4E1"},
				// No parentIndex. Must not be counted as a second (season-0) season.
				map[string]any{"ratingKey": "11", "index": 2.0, "title": "Unparented"},
			},
		},
	})
	f.onJSON("GET", "/library/metadata/10", map[string]any{
		"MediaContainer": map[string]any{
			"Metadata": []any{
				map[string]any{"ratingKey": "10", "Media": []any{
					map[string]any{"Part": []any{
						map[string]any{"id": 500.0, "Stream": []any{
							map[string]any{"id": 1.0, "streamType": 2.0, "languageCode": "eng", "language": "English"},
						}},
					}},
				}},
			},
		},
	})

	root := commands.BuildRoot()
	root.SetArgs([]string{"set-audio", "--show", "Show", "--language", "eng", "--dry-run"})
	out, _ := testutil.Capture(t, func() { _ = root.Execute() })

	got := mustUnmarshal(t, out)
	if errStr, _ := got["error"].(string); strings.Contains(errStr, "spans") {
		t.Fatalf("refused with %q — the show has one season; the unparented episode is not season 0", errStr)
	}
}

func TestAuditAudioNdjsonStreamsRowsThenSummary(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search", showHubResponse("SHOW1", "Foo Show"))
	f.onJSON("GET", "/library/metadata/SHOW1/allLeaves", map[string]any{
		"MediaContainer": map[string]any{
			"Metadata": []any{
				map[string]any{"ratingKey": "1", "parentIndex": 1.0, "index": 1.0, "title": "x"},
			},
		},
	})
	f.onJSON("GET", "/library/metadata/1", map[string]any{
		"MediaContainer": map[string]any{
			"Metadata": []any{
				map[string]any{"ratingKey": "1", "Media": []any{
					map[string]any{"Part": []any{
						map[string]any{"id": 500.0, "Stream": []any{
							map[string]any{"streamType": 2.0, "languageCode": "por", "language": "Portuguese",
								"default": true, "selected": true},
						}},
					}},
				}},
			},
		},
	})

	root := commands.BuildRoot()
	root.SetArgs([]string{"audit-audio", "Foo", "--ndjson"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1; out=%s", code, out)
	}

	lines := splitNonEmpty(out)
	if len(lines) != 2 {
		t.Fatalf("want 2 lines (1 row + summary), got %d: %v", len(lines), lines)
	}
	row := mustUnmarshal(t, lines[0])
	summary := mustUnmarshal(t, lines[1])

	if row["ratingKey"] != "1" || row["defaultAudioCode"] != "por" {
		t.Fatalf("row = %#v", row)
	}
	wantSummary := map[string]any{"ok": true, "show": "Foo Show", "showRatingKey": "SHOW1", "count": 1.0}
	for k, v := range wantSummary {
		if summary[k] != v {
			t.Fatalf("summary[%s] = %#v, want %#v (full: %#v)", k, summary[k], v, summary)
		}
	}
}

// TestAuditAudioEmptyShowIsBadRequest pins the audit-audio migration off
// output.Usage (v1 exit 64) onto the v2 BAD_REQUEST envelope (exit 1).
func TestAuditAudioEmptyShowIsBadRequest(t *testing.T) {
	_ = newFakePMS(t)
	root := commands.BuildRoot()
	root.SetArgs([]string{"audit-audio", "  "})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false", got["ok"])
	}
	body := errBody(t, got)
	if body["code"] != output.CodeBadRequest {
		t.Fatalf("code = %#v, want %q", body["code"], output.CodeBadRequest)
	}
	if body["message"] != "show cannot be empty" {
		t.Fatalf("message = %#v, want %q", body["message"], "show cannot be empty")
	}
}

// TestSetAudioSingleItemMissingLanguageTrackIsTrackNotFound covers the
// single-item set-audio path's track-miss -> PLEX_TRACK_NOT_FOUND, exit 2,
// with the language riding in data — end to end through the command layer
// (internal/streams' own package tests already cover this at the builder
// level; this pins the command-layer FailErr wiring on top of it).
func TestSetAudioSingleItemMissingLanguageTrackIsTrackNotFound(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/library/metadata/123", map[string]any{
		"MediaContainer": map[string]any{
			"Metadata": []any{
				map[string]any{"ratingKey": "123", "Media": []any{
					map[string]any{"Part": []any{
						map[string]any{"id": 900.0, "Stream": []any{
							map[string]any{"id": 1.0, "streamType": 2.0, "languageCode": "deu", "language": "German"},
						}},
					}},
				}},
			},
		},
	})

	root := commands.BuildRoot()
	root.SetArgs([]string{"set-audio", "123", "--language", "eng"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (ExitPlex); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false", got["ok"])
	}
	body := errBody(t, got)
	if body["code"] != output.CodeTrackNotFound {
		t.Fatalf("code = %#v, want %q", body["code"], output.CodeTrackNotFound)
	}
	data, _ := got["data"].(map[string]any)
	if data["language"] != "eng" {
		t.Fatalf("data.language = %#v, want %q", data["language"], "eng")
	}
	if n := f.countMethod("PUT"); n != 0 {
		t.Fatalf("PUT calls = %d, want 0", n)
	}
}

// TestBulkSetAudioPartialFailureIsHTTPErrorWithResults covers the bulk
// partial-failure aggregate: one episode's PUT fails with a non-404 upstream
// error, so per docs/error_model_v2.md §3's resolved STOP-rule exception the
// aggregate is NOT all-track-miss and reads as PLEX_HTTP_ERROR, exit 2, with
// the per-episode results array preserved verbatim under data.results.
func TestBulkSetAudioPartialFailureIsHTTPErrorWithResults(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search", showHubResponse("1", "Show"))
	f.onJSON("GET", "/library/metadata/1/allLeaves", map[string]any{
		"MediaContainer": map[string]any{
			"Metadata": []any{
				map[string]any{"ratingKey": "10", "parentIndex": 1.0, "index": 1.0, "title": "S1E1"},
				map[string]any{"ratingKey": "11", "parentIndex": 1.0, "index": 2.0, "title": "S1E2"},
			},
		},
	})
	f.onJSON("GET", "/library/metadata/10,11", map[string]any{
		"MediaContainer": map[string]any{
			"Metadata": []any{
				map[string]any{"ratingKey": "10", "Media": []any{
					map[string]any{"Part": []any{
						map[string]any{"id": 500.0, "Stream": []any{
							map[string]any{"id": 2.0, "streamType": 2.0, "languageCode": "eng", "language": "English"},
						}},
					}},
				}},
				map[string]any{"ratingKey": "11", "Media": []any{
					map[string]any{"Part": []any{
						map[string]any{"id": 501.0, "Stream": []any{
							map[string]any{"id": 3.0, "streamType": 2.0, "languageCode": "eng", "language": "English"},
						}},
					}},
				}},
			},
		},
	})
	f.onStatus("PUT", "/library/parts/500", 200)
	f.onStatus("PUT", "/library/parts/501", 500)

	root := commands.BuildRoot()
	root.SetArgs([]string{"set-audio", "--show", "Show", "--language", "eng"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (ExitPlex); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false", got["ok"])
	}
	body := errBody(t, got)
	if body["code"] != output.CodeHTTPError {
		t.Fatalf("code = %#v, want %q", body["code"], output.CodeHTTPError)
	}
	data, _ := got["data"].(map[string]any)
	results, _ := data["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("data.results = %#v, want 2 entries", data["results"])
	}
	statuses := map[string]int{}
	for _, r := range results {
		row, _ := r.(map[string]any)
		s, _ := row["status"].(string)
		statuses[s]++
	}
	if statuses["ok"] != 1 || statuses["error"] != 1 {
		t.Fatalf("results statuses = %#v, want 1 ok + 1 error", statuses)
	}
}
