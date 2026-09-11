package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/clients"
	"github.com/corinthian/plexctl/internal/library"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/sessions"
)

func init() {
	Register(func(root *cobra.Command) {
		root.AddCommand(
			newWatchedCmd(),
			newUnwatchedCmd(),
			newRateCmd(),
		)
	})
}

// currentKeyOrFail is the shared no-argument path of watched/unwatched/rate:
// only when the caller omits the ratingKey is a client involved at all.
//
// The invariant, and the fix these three commands carry: the positional
// argument is read FIRST. An explicit ratingKey addresses the library, not a
// device, so it must not depend on one being awake — resolving a client
// before looking at the argument (cli.py's statement order, which this port
// inherited) meant `plexctl watched 12345` failed PLEX_CLIENT_UNKNOWN /
// PLEX_CLIENT_INACTIVE or CLOUD_UNREACHABLE with the Apple TV asleep, for a
// call that needs neither /clients nor plex.tv.
//
// --client alongside an explicit key is therefore inert, not rejected:
// rejecting would break habitual callers that always pass it, for no safety
// gain.
func currentKeyOrFail(clientName string) (string, bool) {
	key := sessions.CurrentRatingKey(clients.Resolve(clientName))
	if key == "" {
		output.FailErr(output.Err(output.CodeNothingPlaying, "nothing playing — provide a ratingKey").WithHint("provide a ratingKey"))
		return "", false
	}
	return key, true
}

func newWatchedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watched [RATING_KEY]",
		Short: "Mark RATING_KEY as watched. Omit to target the currently playing item.",
		Args:  cobra.MaximumNArgs(1),
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		key := ""
		if len(args) == 1 {
			key = args[0]
		}
		if key == "" {
			var ok bool
			if key, ok = currentKeyOrFail(*client); !ok {
				return nil
			}
		}
		output.Out(library.Scrobble(key))
		return nil
	}
	return cmd
}

func newUnwatchedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unwatched [RATING_KEY]",
		Short: "Mark RATING_KEY as unwatched. Omit to target the currently playing item.",
		Args:  cobra.MaximumNArgs(1),
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		key := ""
		if len(args) == 1 {
			key = args[0]
		}
		if key == "" {
			var ok bool
			if key, ok = currentKeyOrFail(*client); !ok {
				return nil
			}
		}
		output.Out(library.Unscrobble(key))
		return nil
	}
	return cmd
}

func newRateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rate RATING [RATING_KEY]",
		Short: "Rate an item RATING (0-10). Omit RATING_KEY to target the currently playing item.",
		Args:  cobra.RangeArgs(1, 2),
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		rating, err := strconv.Atoi(args[0])
		if err != nil || rating < 0 || rating > 10 {
			return fmt.Errorf("invalid value for 'RATING': '%s' is not in the range 0<=x<=10", args[0])
		}
		key := ""
		if len(args) == 2 {
			key = args[1]
		}
		if key == "" {
			var ok bool
			if key, ok = currentKeyOrFail(*client); !ok {
				return nil
			}
		}
		output.Out(library.Rate(key, rating))
		return nil
	}
	return cmd
}
