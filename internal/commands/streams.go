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
				output.FailErr(output.Err(output.CodeBadRequest, "show cannot be empty"))
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
				result["note"] = fmt.Sprintf("no episodes found for: %s", jsonx.PyRepr(show))
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
				output.FailErr(output.Err(output.CodeBadRequest, "--show and --show-rating-key are mutually exclusive"))
				return nil
			}
			if hasRatingKey && bulk {
				output.FailErr(output.Err(output.CodeBadRequest, "provide RATING_KEY (single) or --show/--show-rating-key (bulk), not both"))
				return nil
			}
			if !hasRatingKey && !bulk {
				output.FailErr(output.Err(output.CodeBadRequest, "provide RATING_KEY (single) or --show/--show-rating-key (bulk)"))
				return nil
			}

			if bulk {
				if streamIDSet {
					output.FailErr(output.Err(output.CodeBadRequest, "--stream-id is single-item only; not valid with --show"))
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
				result, cliErr := bulkSetAudio(show, showRKPtr, lang, seasonPtr, allSeasons, onlyNonEng, dryRun)
				if cliErr != nil {
					output.FailErr(cliErr)
					return nil
				}
				output.Out(result)
				return nil
			}

			// single-item
			//
			// NOTE (P2-D scope note): unlike its five siblings above and its
			// set-subtitle twin below, this guard is deliberately left on the
			// legacy output.Out([...]) shape. docs/error_inventory.md's
			// streams.go note names exactly six hand-rolled flag-validation
			// sites as "wrongly exit 1" (3,4,5,6,11,12 in its table) — this
			// site is table row 8, identical in kind and message pattern to
			// row 12 (set-subtitle's --language/--stream-id guard) but is not
			// among the six named in docs/error_model_v2.md §3's migration
			// mapping. Exit code is unaffected either way (Out's falsy branch
			// already exits 1, same as BAD_REQUEST would). Flagged in the P2-D
			// report as a gap worth confirming rather than migrated on
			// judgment call.
			if languageSet && streamIDSet {
				output.Out(jsonx.J{"ok": false, "error": "--language and --stream-id are mutually exclusive"})
				return nil
			}
			if streamIDSet {
				result, cliErr := streams.SetAudioStream(ratingKey, "", &streamID)
				if cliErr != nil {
					output.FailErr(cliErr)
					return nil
				}
				output.Out(result)
				return nil
			}
			lang := language
			if lang == "" {
				lang = "eng"
			}
			result, cliErr := streams.SetAudioStream(ratingKey, lang, nil)
			if cliErr != nil {
				output.FailErr(cliErr)
				return nil
			}
			output.Out(result)
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
//
// On failure it returns a *output.CLIError (v2 coded contract); the command
// layer forwards that to output.FailErr. Success maps (no episodes, dry-run,
// clean bulk apply) are unchanged.
func bulkSetAudio(show string, showRatingKey *string, language string, season *int, allSeasons, onlyNonEng, dryRun bool) (jsonx.J, *output.CLIError) {
	var showKey any
	var title any

	if showRatingKey != nil {
		showKey = *showRatingKey
		meta := library.Metadata(*showRatingKey)
		if jsonx.Truthy(meta) {
			title = meta["title"]
		}
	} else {
		hits, _ := library.SearchTiered(show, "show")
		distinct := map[string]bool{}
		for _, h := range hits {
			distinct[jsonx.AsStr(h["ratingKey"])] = true
		}
		if len(distinct) > 1 {
			seen := map[string]bool{}
			matches := make([]jsonx.J, 0, len(distinct))
			for _, h := range hits {
				rk := jsonx.AsStr(h["ratingKey"])
				if seen[rk] {
					continue
				}
				seen[rk] = true
				matches = append(matches, jsonx.J{"title": h["title"], "ratingKey": rk})
			}
			return nil, output.Err(output.CodeShowAmbiguous,
				fmt.Sprintf("ambiguous show %s — %d series match; pass --show-rating-key", jsonx.PyRepr(show), len(distinct))).
				WithData("matches", matches).
				WithHint("disambiguate with --show-rating-key KEY")
		}
		if len(hits) == 0 {
			return nil, output.Err(output.CodeNotFound,
				fmt.Sprintf("no show found for: %s — pass --show-rating-key", jsonx.PyRepr(show))).
				WithData("query", show)
		}
		showKey = hits[0]["ratingKey"]
		title = hits[0]["title"]
	}

	episodes := library.EpisodesForShowKey(showKey, false, season)
	if len(episodes) == 0 {
		return jsonx.J{"ok": true, "count": 0, "results": []jsonx.J{},
			"note": fmt.Sprintf("no episodes for show %s", jsonx.AsStr(showKey))}, nil
	}

	// Count only episodes that actually declare a season. An unparented episode
	// coerced to 0 invents a Specials season that does not exist — inflating the
	// count that gates this bulk write, and turning a single-season show into a
	// spurious "spans 2 seasons" refusal. Built in the same pass as the season
	// breakdown so both the scope-required error and the success/dry-run
	// results can reuse it.
	seasonSet := map[int]bool{}
	breakdown := jsonx.J{}
	for _, e := range episodes {
		if s, ok := library.SeasonOf(e); ok {
			seasonSet[s] = true
		}
		k := jsonx.AsStr(e["parentIndex"])
		if v, ok := breakdown[k]; ok {
			breakdown[k] = v.(int) + 1
		} else {
			breakdown[k] = 1
		}
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
		return nil, output.Err(output.CodeScopeRequired,
			fmt.Sprintf("%s spans %d seasons %s; pass --season N or --all-seasons",
				jsonx.PyRepr(titleOrKey), len(seasons), formatIntList(seasons))).
			WithData("seasons", breakdown).
			WithHint("add --all-seasons, or narrow with --season N")
	}

	plan := streams.PlanBulkAudio(episodes, language, onlyNonEng)
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
		}, nil
	}

	results, codes := streams.ExecuteBulkAudio(plan)
	applied, failed, skipped := 0, 0, 0
	allTrackMiss := true
	for i, r := range results {
		switch r["status"] {
		case "ok":
			applied++
		case "error":
			failed++
			if codes[i] != output.CodeNotFound {
				allTrackMiss = false
			}
		case "skipped":
			skipped++
		}
	}
	if failed > 0 {
		// §3's resolved STOP-rule exception: if every per-episode PUT failure
		// classified as a 404 (the target part/track vanished between
		// planning and execution — the bulk-write analogue of a track miss),
		// the aggregate reads as PLEX_TRACK_NOT_FOUND; any other failure
		// shape (5xx, transport, etc.) reads as the generic PLEX_HTTP_ERROR.
		// Either way the per-episode results array survives verbatim.
		code := output.CodeHTTPError
		if allTrackMiss {
			code = output.CodeTrackNotFound
		}
		return nil, output.Err(code, fmt.Sprintf("%d of %d episodes failed to set audio", failed, applied+failed+skipped)).
			WithData("results", results)
	}
	return jsonx.J{
		"ok": true, "show": title, "ratingKey": jsonx.AsStr(showKey),
		"seasons": breakdown, "applied": applied, "skipped": skipped, "failed": failed, "results": results,
	}, nil
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
				output.FailErr(output.Err(output.CodeBadRequest, "--off and --language/--stream-id are mutually exclusive"))
				return nil
			}
			if languageSet && streamIDSet {
				output.FailErr(output.Err(output.CodeBadRequest, "--language and --stream-id are mutually exclusive"))
				return nil
			}
			if off {
				result, cliErr := streams.SetSubtitleStream(ratingKey, "", nil, true)
				if cliErr != nil {
					output.FailErr(cliErr)
					return nil
				}
				output.Out(result)
				return nil
			}
			if streamIDSet {
				result, cliErr := streams.SetSubtitleStream(ratingKey, "", &streamID, false)
				if cliErr != nil {
					output.FailErr(cliErr)
					return nil
				}
				output.Out(result)
				return nil
			}
			lang := language
			if lang == "" {
				lang = "eng"
			}
			result, cliErr := streams.SetSubtitleStream(ratingKey, lang, nil, false)
			if cliErr != nil {
				output.FailErr(cliErr)
				return nil
			}
			output.Out(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&language, "language", "", "Subtitle language code (default: eng)")
	cmd.Flags().IntVar(&streamID, "stream-id", 0, "Explicit subtitle stream id")
	cmd.Flags().BoolVar(&off, "off", false, "Disable subtitles")
	return cmd
}
