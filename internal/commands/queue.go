package commands

import (
	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/clients"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/playback"
	"github.com/corinthian/plexctl/internal/queue"
	"github.com/corinthian/plexctl/internal/queuestate"
)

func init() {
	Register(func(root *cobra.Command) {
		root.AddCommand(
			newQueueCmd(),
			newQueueStartCmd(),
			newQueueShowCmd(),
			newQueueShuffleCmd(),
			newQueueUnshuffleCmd(),
			newQueueClearCmd(),
			newQueueRemoveCmd(),
			newQueueAddCmd(),
		)
	})
}

func newQueueCmd() *cobra.Command {
	var shuffle bool
	var repeat bool
	cmd := &cobra.Command{
		Use:   "queue RATING_KEYS...",
		Short: "Create a play queue from one or more RATING_KEYS and start playing immediately.",
		Args:  cobra.MinimumNArgs(1),
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		q := queue.Create(args, shuffle, repeat)
		if !jsonx.Truthy(q["ok"]) {
			output.Out(q)
			return nil
		}
		target := clients.Resolve(*client)
		result := playback.PlayQueue(target, jsonx.AsStr(q["playQueueID"]), jsonx.AsStr(q["selectedItemID"]))
		// The queue exists on the server the moment Create succeeded. Surface
		// its IDs and save state whether or not the bind succeeded, so a bind
		// failure leaves a *staged* queue that queue-start can bind later —
		// not an orphan the caller can't see or recover. clientUnreachable
		// flags a transport-shaped bind failure (device didn't answer), never
		// an HTTP-error bind. The success path stays byte-identical.
		result["playQueueID"] = q["playQueueID"]
		result["selectedItemID"] = q["selectedItemID"]
		if !jsonx.Truthy(result["ok"]) {
			if errStr, _ := result["error"].(string); playback.IsTransportError(errStr) {
				result["clientUnreachable"] = true
			}
		}
		if mid := target["machineIdentifier"]; jsonx.Truthy(mid) {
			queuestate.Save(jsonx.AsStr(mid), jsonx.AsStr(q["playQueueID"]), jsonx.AsStr(q["selectedItemID"]))
		}
		output.Out(result)
		return nil
	}
	cmd.Flags().BoolVar(&shuffle, "shuffle", false, "Shuffle the queue before playing")
	cmd.Flags().BoolVar(&repeat, "repeat", false, "Repeat the queue when finished")
	return cmd
}

func newQueueStartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue-start",
		Short: "Bind the saved/staged play queue to the client and start playing (recovery after a bind failure).",
		Args:  cobra.NoArgs,
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		// Binding needs a live client with a baseurl, so use the full resolver.
		output.Out(queue.Start(clients.Resolve(*client)))
		return nil
	}
	return cmd
}

func newQueueShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue-show",
		Short: "Show current play queue. Returns items with playQueueItemID, title, and type.",
		Args:  cobra.NoArgs,
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		output.Out(queue.Show(clients.Resolve(*client)))
		return nil
	}
	return cmd
}

func newQueueShuffleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue-shuffle",
		Short: "Shuffle the current play queue.",
		Args:  cobra.NoArgs,
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		output.Out(queue.Shuffle(clients.Resolve(*client)))
		return nil
	}
	return cmd
}

func newQueueUnshuffleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue-unshuffle",
		Short: "Turn off shuffle on the current play queue.",
		Args:  cobra.NoArgs,
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		output.Out(queue.Unshuffle(clients.Resolve(*client)))
		return nil
	}
	return cmd
}

func newQueueClearCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue-clear",
		Short: "Remove all items from the current play queue.",
		Args:  cobra.NoArgs,
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		output.Out(queue.Clear(clients.Resolve(*client)))
		return nil
	}
	return cmd
}

func newQueueRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue-remove ITEM_ID",
		Short: "Remove ITEM_ID from the current play queue. ITEM_ID is playQueueItemID from queue-show.",
		Args:  cobra.ExactArgs(1),
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		output.Out(queue.RemoveItem(clients.Resolve(*client), args[0]))
		return nil
	}
	return cmd
}

func newQueueAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "queue-add RATING_KEYS...",
		Short: "Append one or more RATING_KEYS to the active play queue on the target client.",
		Args:  cobra.MinimumNArgs(1),
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		output.Out(queue.AddToClient(clients.Resolve(*client), args))
		return nil
	}
	return cmd
}
