package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/clients"
	"github.com/corinthian/plexctl/internal/jsonx"
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

// resolveTargetKey mirrors the shared shape of watched/unwatched/rate: the
// client is resolved BEFORE the ratingKey argument is consulted (matches
// cli.py's statement order), and an idle client with no explicit ratingKey
// is the one guarded failure.
func resolveTargetKey(client jsonx.J, explicit string) (string, bool) {
	if explicit != "" {
		return explicit, true
	}
	key := sessions.CurrentRatingKey(client)
	if key == "" {
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
		target := clients.Resolve(*client)
		var explicit string
		if len(args) == 1 {
			explicit = args[0]
		}
		key, ok := resolveTargetKey(target, explicit)
		if !ok {
			output.Out(jsonx.J{"ok": false, "error": "nothing playing — provide a ratingKey"})
			return nil
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
		target := clients.Resolve(*client)
		var explicit string
		if len(args) == 1 {
			explicit = args[0]
		}
		key, ok := resolveTargetKey(target, explicit)
		if !ok {
			output.Out(jsonx.J{"ok": false, "error": "nothing playing — provide a ratingKey"})
			return nil
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
		target := clients.Resolve(*client)
		var explicit string
		if len(args) == 2 {
			explicit = args[1]
		}
		key, ok := resolveTargetKey(target, explicit)
		if !ok {
			output.Out(jsonx.J{"ok": false, "error": "nothing playing — provide a ratingKey"})
			return nil
		}
		output.Out(library.Rate(key, rating))
		return nil
	}
	return cmd
}
