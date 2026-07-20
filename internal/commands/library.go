package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/clients"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/library"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/playback"
)

func init() {
	Register(func(root *cobra.Command) {
		root.AddCommand(
			newSearchCmd(),
			newLibraryCmd(),
			newMetadataCmd(),
			newPlayLatestCmd(),
			newEpisodesCmd(),
		)
	})
}

// searchMinScoreEnvWarning is appended to a search success envelope when
// $PLEXCTL_SEARCH_MIN_SCORE is set at runtime. v2 removed --min-score (and
// the env override that used to pin a single relevance floor) — the env var
// is now inert, so a stale one in the caller's shell is surfaced as a
// warning rather than silently doing nothing.
func searchMinScoreEnvWarning(out jsonx.J) jsonx.J {
	if _, ok := os.LookupEnv("PLEXCTL_SEARCH_MIN_SCORE"); ok {
		out = output.Warn(out, output.CodeBadRequest, "PLEXCTL_SEARCH_MIN_SCORE is ignored in v2", "remove the env var")
	}
	return out
}

func newSearchCmd() *cobra.Command {
	var mediaType string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Search the library for QUERY. Returns ratingKey, title, type, and year per result.",
		Long: `Search the library for QUERY. Returns ratingKey, title, type, and year per result.

Use --type to restrict to show, movie, or episode. Use --json for full metadata.

By default, search returns confident matches only, and widens to weaker ones just
when nothing confident exists — those carry "loose": true, meaning the hit may be
noise.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := choiceError(cmd, "type", mediaType, "show", "movie", "episode"); err != nil {
				return err
			}
			query := args[0]
			if strings.TrimSpace(query) == "" {
				output.FailErr(output.Err(output.CodeBadRequest, "query cannot be empty"))
				return nil
			}
			results, loose := library.SearchTiered(query, mediaType)
			if asJSON {
				out := jsonx.J{"ok": true, "results": results}
				if loose && len(results) > 0 {
					out["loose"] = true
				}
				output.Print(searchMinScoreEnvWarning(out))
				return nil
			}
			if len(results) == 0 {
				output.Print(searchMinScoreEnvWarning(jsonx.J{"ok": true, "results": []jsonx.J{}, "note": "no matches"}))
				return nil
			}
			summary := make([]jsonx.J, 0, len(results))
			for _, r := range results {
				summary = append(summary, jsonx.J{
					"title":     r["title"],
					"type":      r["type"],
					"ratingKey": r["ratingKey"],
					"year":      r["year"],
				})
			}
			out := jsonx.J{"ok": true, "results": summary}
			if loose {
				out["loose"] = true
				out["note"] = "low-confidence match — no result cleared the confident threshold"
			}
			output.Print(searchMinScoreEnvWarning(out))
			return nil
		},
	}
	cmd.Flags().StringVar(&mediaType, "type", "", "")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit full metadata JSON")
	return cmd
}

// --- library group -----------------------------------------------------------

func newLibraryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "library",
		Short: "Library browsing commands.",
	}
	cmd.AddCommand(newLibrarySectionsCmd(), newLibraryListCmd())
	return cmd
}

func newLibrarySectionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sections",
		Short: "List library sections with their section IDs.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			output.Out(jsonx.J{"ok": true, "sections": library.Sections()})
			return nil
		},
	}
}

func newLibraryListCmd() *cobra.Command {
	var section string
	var mediaType string
	var unwatched bool
	var sortFlag string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List items in a section, optionally filtered by type or watch status.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := choiceError(cmd, "type", mediaType, "show", "movie"); err != nil {
				return err
			}
			items := library.ListSection(section, mediaType, unwatched, sortFlag)
			output.Out(jsonx.J{"ok": true, "count": len(items), "items": items})
			return nil
		},
	}
	cmd.Flags().StringVarP(&section, "section", "s", "", "Section ID (see `plexctl library sections`)")
	_ = cmd.MarkFlagRequired("section")
	cmd.Flags().StringVar(&mediaType, "type", "", "")
	cmd.Flags().BoolVar(&unwatched, "unwatched", false, "")
	cmd.Flags().StringVar(&sortFlag, "sort", "", "e.g. addedAt:desc, titleSort:asc")
	return cmd
}

// --- metadata ------------------------------------------------------------

func newMetadataCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "metadata RATING_KEY",
		Short: "Fetch full metadata for RATING_KEY (includes streams, chapters, ratings).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ratingKey := args[0]
			item := library.Metadata(ratingKey)
			if !jsonx.Truthy(item) {
				output.FailErr(output.Err(output.CodeNotFound, fmt.Sprintf("no metadata found for ratingKey: %s", ratingKey)).
					WithData("ratingKey", ratingKey))
				return nil
			}
			output.Out(jsonx.J{"ok": true, "metadata": item})
			return nil
		},
	}
}

// --- play-latest -----------------------------------------------------------

func newPlayLatestCmd() *cobra.Command {
	var client string
	var unwatched bool
	var keyOnly bool
	cmd := &cobra.Command{
		Use:   "play-latest QUERY",
		Short: "Play the next unwatched episode of a show matching QUERY, or a movie if no show found.",
		Long: `Play the next unwatched episode of a show matching QUERY, or a movie if no show found.

Use --unwatched to force the next unwatched episode even if in-progress exists.
Use --key-only to resolve the ratingKey without starting playback.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]
			item := library.LatestUnwatchedEpisode(query, unwatched)
			if item == nil {
				if unwatched {
					e := output.Err(output.CodeAllWatched, fmt.Sprintf("no unwatched episodes for: %s", jsonx.PyRepr(query))).
						WithHint("drop --unwatched to replay, or pick another show")
					if hit := library.ResolveShow(query); hit != nil {
						e = e.WithData("show", hit["title"]).WithData("showRatingKey", jsonx.AsStr(hit["ratingKey"]))
					}
					output.FailErr(e)
					return nil
				}
				movies, _ := library.SearchTiered(query, "movie")
				if len(movies) == 0 {
					output.FailErr(output.Err(output.CodeNotFound, fmt.Sprintf("nothing found for: %s", jsonx.PyRepr(query))).
						WithData("query", query))
					return nil
				}
				item = movies[0]
			}
			if keyOnly {
				output.Out(jsonx.J{
					"ok":        true,
					"ratingKey": item["ratingKey"],
					"title":     item["title"],
					"type":      item["type"],
					"season":    item["parentIndex"],
					"episode":   item["index"],
					"year":      item["year"],
				})
				return nil
			}
			target := clients.Resolve(client)
			result, cliErr := playback.PlayMedia(target, jsonx.AsStr(item["ratingKey"]))
			if cliErr != nil {
				output.FailErr(cliErr)
				return nil
			}
			result["playing"] = jsonx.J{
				"title":     item["title"],
				"type":      item["type"],
				"season":    item["parentIndex"],
				"episode":   item["index"],
				"year":      item["year"],
				"ratingKey": item["ratingKey"],
			}
			output.Out(result)
			return nil
		},
	}
	cmd.Flags().StringVarP(&client, "client", "c", "", "")
	cmd.Flags().BoolVar(&unwatched, "unwatched", false, "Force next unwatched episode")
	cmd.Flags().BoolVar(&keyOnly, "key-only", false, "Resolve ratingKey without starting playback")
	return cmd
}

// --- episodes ----------------------------------------------------------------

func newEpisodesCmd() *cobra.Command {
	var unwatched bool
	var season int
	var asJSON bool
	var ndjson bool
	cmd := &cobra.Command{
		Use:   "episodes SHOW",
		Short: "List episodes of SHOW, ordered by (season, episode).",
		Long: `List episodes of SHOW, ordered by (season, episode).

Default output is one row per episode with the common columns. Use --json for
full metadata, --season N to restrict to a season, --unwatched for unwatched
only, --ndjson to stream line-delimited rows for batch callers.`,
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
			eps := []jsonx.J{}
			if hit != nil {
				eps = library.EpisodesForShowKey(hit["ratingKey"], unwatched, seasonPtr)
			}

			var rows []jsonx.J
			if asJSON {
				rows = eps
			} else {
				rows = make([]jsonx.J, 0, len(eps))
				for _, e := range eps {
					viewCount, ok := e["viewCount"]
					if !ok {
						viewCount = 0
					}
					rows = append(rows, jsonx.J{
						"ratingKey":        e["ratingKey"],
						"grandparentTitle": e["grandparentTitle"],
						"parentIndex":      e["parentIndex"],
						"index":            e["index"],
						"title":            e["title"],
						"viewCount":        viewCount,
						"duration":         e["duration"],
						"year":             e["year"],
					})
				}
			}

			if ndjson {
				output.EmitNDJSON(seqOf(rows), showIdentity(jsonx.J{"ok": true}, hit))
				return nil
			}
			if asJSON {
				output.Print(showIdentity(jsonx.J{"ok": true, "count": len(rows), "results": rows}, hit))
				return nil
			}
			result := showIdentity(jsonx.J{"ok": true, "count": len(rows), "episodes": rows}, hit)
			if len(rows) == 0 {
				result["note"] = fmt.Sprintf("no episodes found for: %s", jsonx.PyRepr(show))
			}
			output.Out(result)
			return nil
		},
	}
	cmd.Flags().BoolVar(&unwatched, "unwatched", false, "Only unwatched episodes")
	cmd.Flags().IntVar(&season, "season", 0, "Restrict to one season number")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit full episode metadata")
	cmd.Flags().BoolVar(&ndjson, "ndjson", false, "Stream one JSON object per episode, then a summary line")
	return cmd
}
