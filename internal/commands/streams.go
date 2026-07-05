package commands

import (
	"fmt"
	"iter"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/library"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/streams"
)

func init() {
	Register(func(root *cobra.Command) {
		root.AddCommand(
			newAuditAudioCmd(),
			newSetAudioCmd(),
			newSetSubtitleCmd(),
		)
	})
}

// --- audit-audio -------------------------------------------------------------

func newAuditAudioCmd() *cobra.Command {
	var language string
	var season int
	var ndjson bool
	cmd := &cobra.Command{
		Use:   "audit-audio SHOW",
		Short: "Audit each episode's audio tracks for SHOW.",
		Long: `Audit each episode's audio tracks for SHOW.

Reports the default and selected audio language per episode, flags episodes
whose default audio is not the preferred --language, and whether a
preferred-language alternate track exists. --ndjson streams rows as each
metadata chunk completes, so partial progress survives a killed caller.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			show := args[0]
			if strings.TrimSpace(show) == "" {
				output.Out(jsonx.J{"ok": false, "error": "show cannot be empty"})
				return nil
			}
			var seasonPtr *int
			if cmd.Flags().Changed("season") {
				seasonPtr = &season
			}
			hit := library.ResolveShow(show)
			var rowsIter iter.Seq[jsonx.J]
			if hit != nil {
				rowsIter = streams.AuditAudioForKey(hit["ratingKey"], language, seasonPtr)
			} else {
				rowsIter = seqOf(nil)
			}

			if ndjson {
				// Stream the iterator through directly — do NOT collect first,
				// so a killed caller keeps partial progress (mirrors
				// cli._emit_ndjson consuming the generator lazily).
				output.EmitNDJSON(rowsIter, showIdentity(jsonx.J{"ok": true}, hit))
				return nil
			}

			rows := []jsonx.J{}
			for r := range rowsIter {
				rows = append(rows, r)
			}
			result := showIdentity(jsonx.J{"ok": true, "count": len(rows), "audit": rows}, hit)
			if len(rows) == 0 {
				result["note"] = fmt.Sprintf("no episodes found for: '%s'", show)
			}
			output.Out(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&language, "language", "eng", "Preferred audio language code (default: eng)")
	cmd.Flags().IntVar(&season, "season", 0, "Restrict to one season number")
	cmd.Flags().BoolVar(&ndjson, "ndjson", false, "Stream one JSON object per episode, then a summary line")
	return cmd
}

// --- set-audio -----------------------------------------------------------

func newSetAudioCmd() *cobra.Command {
	var show string
	var showRatingKey string
	var language string
	var streamID int
	var season int
	var allSeasons bool
	var onlyNonEng bool
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "set-audio [RATING_KEY]",
		Short: "Select the audio track on RATING_KEY (single) or across --show / --show-rating-key (bulk).",
		Long: `Select the audio track on RATING_KEY (single) or across --show / --show-rating-key (bulk).

Single: --language (default eng) xor --stream-id. Bulk: --language, optional
--season / --all-seasons, --only-non-eng, --dry-run.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hasRatingKey := len(args) == 1
			var ratingKey string
			if hasRatingKey {
				ratingKey = args[0]
			}

			showSet := cmd.Flags().Changed("show")
			showRKSet := cmd.Flags().Changed("show-rating-key")
			streamIDSet := cmd.Flags().Changed("stream-id")
			languageSet := cmd.Flags().Changed("language")
			bulk := showSet || showRKSet

			if showSet && showRKSet {
				output.Out(jsonx.J{"ok": false, "error": "--show and --show-rating-key are mutually exclusive"})
				return nil
			}
			if hasRatingKey && bulk {
				output.Out(jsonx.J{"ok": false, "error": "provide RATING_KEY (single) or --show/--show-rating-key (bulk), not both"})
				return nil
			}
			if !hasRatingKey && !bulk {
				output.Out(jsonx.J{"ok": false, "error": "provide RATING_KEY (single) or --show/--show-rating-key (bulk)"})
				return nil
			}

			if bulk {
				if streamIDSet {
					output.Out(jsonx.J{"ok": false, "error": "--stream-id is single-item only; not valid with --show"})
					return nil
				}
				lang := language
				if lang == "" {
					lang = "eng"
				}
				var seasonPtr *int
				if cmd.Flags().Changed("season") {
					seasonPtr = &season
				}
				var showRKPtr *string
				if showRKSet {
					showRKPtr = &showRatingKey
				}
				output.Out(bulkSetAudio(show, showRKPtr, lang, seasonPtr, allSeasons, onlyNonEng, dryRun))
				return nil
			}

			// single-item
			if languageSet && streamIDSet {
				output.Out(jsonx.J{"ok": false, "error": "--language and --stream-id are mutually exclusive"})
				return nil
			}
			if streamIDSet {
				output.Out(streams.SetAudioStream(ratingKey, "", &streamID))
				return nil
			}
			lang := language
			if lang == "" {
				lang = "eng"
			}
			output.Out(streams.SetAudioStream(ratingKey, lang, nil))
			return nil
		},
	}
	cmd.Flags().StringVar(&show, "show", "", "Bulk: set audio across a show (best-match)")
	cmd.Flags().StringVar(&showRatingKey, "show-rating-key", "", "Bulk: set audio across a show by authoritative ratingKey")
	cmd.Flags().StringVar(&language, "language", "", "Audio language code (default: eng)")
	cmd.Flags().IntVar(&streamID, "stream-id", 0, "Single-item: explicit audio stream id")
	cmd.Flags().IntVar(&season, "season", 0, "Bulk: restrict to one season")
	cmd.Flags().BoolVar(&allSeasons, "all-seasons", false, "Bulk: acknowledge a multi-season write")
	cmd.Flags().BoolVar(&onlyNonEng, "only-non-eng", false, "Bulk: skip episodes already on the preferred language")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Bulk: print the plan, write nothing")
	return cmd
}

// bulkSetAudio mirrors cli._bulk_set_audio. D4 guards live here: --show
// ambiguity resolves before enumeration; the season-scope guard is orthogonal
// to resolution and applies to both --show and --show-rating-key.
func bulkSetAudio(show string, showRatingKey *string, language string, season *int, allSeasons, onlyNonEng, dryRun bool) jsonx.J {
	var showKey any
	var title any

	if showRatingKey != nil {
		showKey = *showRatingKey
		meta := library.Metadata(*showRatingKey)
		if jsonx.Truthy(meta) {
			title = meta["title"]
		}
	} else {
		hits := library.Search(show, "show", 1.0)
		distinct := map[string]bool{}
		for _, h := range hits {
			distinct[jsonx.AsStr(h["ratingKey"])] = true
		}
		if len(distinct) > 1 {
			return jsonx.J{"ok": false, "error": fmt.Sprintf("ambiguous show '%s' — %d series match; pass --show-rating-key", show, len(distinct))}
		}
		if len(hits) == 0 {
			return jsonx.J{"ok": false, "error": fmt.Sprintf("no show found for: '%s' — pass --show-rating-key", show)}
		}
		showKey = hits[0]["ratingKey"]
		title = hits[0]["title"]
	}

	episodes := library.EpisodesForShowKey(showKey, false, season)
	if len(episodes) == 0 {
		return jsonx.J{"ok": true, "count": 0, "results": []jsonx.J{},
			"note": fmt.Sprintf("no episodes for show %s", jsonx.AsStr(showKey))}
	}

	seasonSet := map[int]bool{}
	for _, e := range episodes {
		seasonSet[int(jsonx.Num(e["parentIndex"]))] = true
	}
	seasons := make([]int, 0, len(seasonSet))
	for s := range seasonSet {
		seasons = append(seasons, s)
	}
	sort.Ints(seasons)

	if season == nil && len(seasons) > 1 && !allSeasons {
		titleOrKey := jsonx.AsStr(showKey)
		if jsonx.Truthy(title) {
			titleOrKey = jsonx.AsStr(title)
		}
		return jsonx.J{"ok": false, "error": fmt.Sprintf("'%s' spans %d seasons %s; pass --season N or --all-seasons",
			titleOrKey, len(seasons), formatIntList(seasons))}
	}

	plan := streams.PlanBulkAudio(episodes, language, onlyNonEng)
	breakdown := jsonx.J{}
	for _, e := range episodes {
		k := jsonx.AsStr(e["parentIndex"])
		if v, ok := breakdown[k]; ok {
			breakdown[k] = v.(int) + 1
		} else {
			breakdown[k] = 1
		}
	}
	toApply := 0
	for _, r := range plan {
		if !jsonx.Truthy(r["skip"]) {
			toApply++
		}
	}

	if dryRun {
		return jsonx.J{
			"ok": true, "dryRun": true, "show": title, "ratingKey": jsonx.AsStr(showKey),
			"seasons": breakdown, "count": len(plan), "toApply": toApply, "plan": plan,
		}
	}

	results := streams.ExecuteBulkAudio(plan)
	applied, failed, skipped := 0, 0, 0
	for _, r := range results {
		switch r["status"] {
		case "ok":
			applied++
		case "error":
			failed++
		case "skipped":
			skipped++
		}
	}
	return jsonx.J{
		"ok": failed == 0, "show": title, "ratingKey": jsonx.AsStr(showKey),
		"seasons": breakdown, "applied": applied, "skipped": skipped, "failed": failed, "results": results,
	}
}

// --- set-subtitle --------------------------------------------------------

func newSetSubtitleCmd() *cobra.Command {
	var language string
	var streamID int
	var off bool
	cmd := &cobra.Command{
		Use:   "set-subtitle RATING_KEY",
		Short: "Select the subtitle track on RATING_KEY by --language/--stream-id, or --off to disable.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ratingKey := args[0]
			languageSet := cmd.Flags().Changed("language")
			streamIDSet := cmd.Flags().Changed("stream-id")

			if off && (languageSet || streamIDSet) {
				output.Out(jsonx.J{"ok": false, "error": "--off and --language/--stream-id are mutually exclusive"})
				return nil
			}
			if languageSet && streamIDSet {
				output.Out(jsonx.J{"ok": false, "error": "--language and --stream-id are mutually exclusive"})
				return nil
			}
			if off {
				output.Out(streams.SetSubtitleStream(ratingKey, "", nil, true))
				return nil
			}
			if streamIDSet {
				output.Out(streams.SetSubtitleStream(ratingKey, "", &streamID, false))
				return nil
			}
			lang := language
			if lang == "" {
				lang = "eng"
			}
			output.Out(streams.SetSubtitleStream(ratingKey, lang, nil, false))
			return nil
		},
	}
	cmd.Flags().StringVar(&language, "language", "", "Subtitle language code (default: eng)")
	cmd.Flags().IntVar(&streamID, "stream-id", 0, "Explicit subtitle stream id")
	cmd.Flags().BoolVar(&off, "off", false, "Disable subtitles")
	return cmd
}
