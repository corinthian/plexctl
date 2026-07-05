package commands

import (
	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/clients"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/playback"
)

func init() {
	Register(func(root *cobra.Command) {
		root.AddCommand(newPlayMediaCmd())
	})
}

func newPlayMediaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "play-media RATING_KEY",
		Short: "Play a specific item by RATING_KEY. Use `search` or `metadata` to find ratingKeys.",
		Args:  cobra.ExactArgs(1),
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		output.Out(playback.PlayMedia(clients.Resolve(*client), args[0]))
		return nil
	}
	return cmd
}
