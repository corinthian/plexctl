package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/clients"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/sessions"
)

func init() {
	Register(func(root *cobra.Command) {
		root.AddCommand(
			newNowPlayingCmd(),
			newContinueWatchingCmd(),
			newHistoryCmd(),
			newContextCmd(),
		)
	})
}

func newNowPlayingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "now-playing",
		Short: "Show what's currently playing. Returns title, type, progress, duration, and ratingKey.",
		Args:  cobra.NoArgs,
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		output.Out(sessions.NowPlaying(clients.Resolve(*client)))
		return nil
	}
	return cmd
}

func newContinueWatchingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "continue-watching",
		Short: "List the continue-watching shelf.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			output.Out(sessions.ContinueWatching())
			return nil
		},
	}
}

func newHistoryCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show recent watch history.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 1 {
				return fmt.Errorf("invalid value for '--limit': '%d' is not greater than 0", limit)
			}
			output.Out(sessions.History(limit))
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "Number of entries to return (must be >= 1)")
	return cmd
}

func newContextCmd() *cobra.Command {
	var historyLimit int
	var noHistory bool
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Bundled snapshot — now-playing + queue + history in one parallel fetch.",
		Args:  cobra.NoArgs,
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if historyLimit < 0 || historyLimit > 10 {
			return fmt.Errorf("invalid value for '--history-limit': '%d' is not in the range 0<=x<=10", historyLimit)
		}
		output.Out(sessions.Context(clients.Resolve(*client), historyLimit, !noHistory))
		return nil
	}
	cmd.Flags().IntVar(&historyLimit, "history-limit", 5, "Number of history entries to bundle (0-10)")
	cmd.Flags().BoolVar(&noHistory, "no-history", false, "Skip history fetch entirely")
	return cmd
}
