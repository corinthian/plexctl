package commands

import (
	"iter"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/jsonx"
)

// addClientFlag registers the --client/-c flag shared by nearly every
// transport/session/queue command (cli.py's --client/-c option repeated on
// each command).
func addClientFlag(cmd *cobra.Command) *string {
	client := new(string)
	cmd.Flags().StringVarP(client, "client", "c", "", "Target client name (default: Apple TV)")
	return client
}

// defaultMinScore mirrors cli._default_min_score: resolved at invocation
// time (Python's callable default), not at flag-registration time, so
// $PLEXCTL_SEARCH_MIN_SCORE is honored per run.
func defaultMinScore() float64 {
	raw, ok := os.LookupEnv("PLEXCTL_SEARCH_MIN_SCORE")
	if !ok {
		return 1.0
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 1.0
	}
	return f
}

// showIdentity mirrors cli._show_identity: echo the resolved show onto a
// result envelope so callers can confirm which series a fuzzy query bound
// to.
func showIdentity(result jsonx.J, hit jsonx.J) jsonx.J {
	if hit != nil {
		result["show"] = hit["title"]
		result["showRatingKey"] = jsonx.AsStr(hit["ratingKey"])
	}
	return result
}

// seqOf adapts a materialized slice to iter.Seq[jsonx.J] for output.EmitNDJSON
// call sites that already have a []jsonx.J in hand (episodes' non-streaming
// row list, reused for its own --ndjson streaming path).
func seqOf(rows []jsonx.J) iter.Seq[jsonx.J] {
	return func(yield func(jsonx.J) bool) {
		for _, r := range rows {
			if !yield(r) {
				return
			}
		}
	}
}

// formatIntList renders []int the way Python's str() renders a list of ints:
// "[1, 2, 3]" — comma+space, no trailing comma, no decimals.
func formatIntList(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = strconv.Itoa(n)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
