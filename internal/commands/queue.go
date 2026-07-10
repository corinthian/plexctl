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
		// Resolve BEFORE Create so an unresolvable/inactive client exits before
		// any playQueue exists on the server (finding 2 — Create-then-Resolve
		// orphaned the created queue with no IDs in output). In production
		// Resolve print-and-exits via os.Exit, so the guard below is
		// unreachable; under the test seam output.Exit records the code and
		// RETURNS an empty dict, so without the guard execution would fall
		// through into Create and leak a server-side queue. queue is the one
		// command whose seam fall-through creates server state.
		target := clients.Resolve(*client)
		if len(target) == 0 {
			return nil
		}
		q := queue.Create(args, shuffle, repeat)
		if !jsonx.Truthy(q["ok"]) {
			output.Out(q)
			return nil
		}
		result := playback.PlayQueue(target, jsonx.AsStr(q["playQueueID"]), jsonx.AsStr(q["selectedItemID"]))
		// The queue exists on the server the moment Create succeeded. Surface
		// its IDs so a bind failure leaves a queue the caller can see and
		// recover, not an orphan. clientUnreachable flags a transport-shaped
		// bind failure (device didn't answer), never an HTTP-error bind. The
		// success path stays byte-identical.
		queue.AnnotateBind(result, jsonx.AsStr(q["playQueueID"]), jsonx.AsStr(q["selectedItemID"]))
		if mid := target["machineIdentifier"]; jsonx.Truthy(mid) {
			midStr := jsonx.AsStr(mid)
			qid := jsonx.AsStr(q["playQueueID"])
			sel := jsonx.AsStr(q["selectedItemID"])
			if jsonx.Truthy(result["ok"]) {
				// Bind succeeded: this IS the live queue, record it
				// unconditionally. D2 ruling: a failed write here does NOT
				// become output.Fail -- playback is already running, so
				// escalating to ok:false would be a worse lie than the one
				// W10 exists to remove. stateSaved is always present (never
				// inferred from absence) so a consumer can tell the two
				// cases apart.
				if serr := queuestate.Save(midStr, qid, sel); serr != nil {
					result["stateSaved"] = false
				} else {
					result["stateSaved"] = true
				}
			} else {
				// Bind failed and no queue was recorded for this client: stage the
				// new queue so queue-start can bind it later (finding 1). If an
				// entry already exists (the bound/playing queue), SaveIfAbsent
				// preserves it and returns false -- no staged key; recovery is
				// re-running `queue` once the device is back. staged derives from
				// the write itself, never a separate Load, so it can't disagree
				// with what was persisted: it is set only when SaveIfAbsent
				// itself reports a successful write.
				staged, serr := queuestate.SaveIfAbsent(midStr, qid, sel)
				if serr != nil {
					// The bind already failed (ok is already false here); staging
					// also failing is a second, distinct problem worth surfacing --
					// playQueueID is already on the envelope via AnnotateBind above.
					result["error"] = jsonx.AsStr(result["error"]) + "; additionally failed to stage queue state: " + serr.Error()
				} else if staged {
					result["staged"] = true
				}
			}
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
