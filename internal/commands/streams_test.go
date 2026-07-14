package commands_test

import (
	"strings"
	"testing"

	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/testutil"
)

// TestSetAudioSetSubtitleGuardStrings covers every mutually-exclusive-flag
// guard verbatim, in cli.py's exact wording. None of these paths touch the
// network — the guard fires before any domain call.
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
			"single-language-and-stream-id-exclusive",
			[]string{"set-audio", "123", "--language", "eng", "--stream-id", "5"},
			"--language and --stream-id are mutually exclusive",
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
			if got["ok"] != false || got["error"] != tc.want {
				t.Fatalf("got %#v, want error=%q", got, tc.want)
			}
		})
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
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	errStr, _ := got["error"].(string)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false", got["ok"])
	}
	if want := "ambiguous show 'Tom and Jerry' — 2 series match; pass --show-rating-key"; errStr != want {
		t.Fatalf("error = %q, want %q", errStr, want)
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
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	errStr, _ := got["error"].(string)
	if got["ok"] != false {
		t.Fatalf("ok = %#v, want false", got["ok"])
	}
	if want := "no show found for: 'Nope' — pass --show-rating-key"; errStr != want {
		t.Fatalf("error = %q, want %q", errStr, want)
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
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	errStr, _ := got["error"].(string)
	want := "'Show' spans 2 seasons [4, 5]; pass --season N or --all-seasons"
	if errStr != want {
		t.Fatalf("error = %q, want %q", errStr, want)
	}
	if n := f.countMethod("PUT"); n != 0 {
		t.Fatalf("PUT calls = %d, want 0 (scope guard must refuse before any write)", n)
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
