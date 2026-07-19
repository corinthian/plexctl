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

// playbackNotStartedErr builds the shared v2 CodePlaybackNotStarted refusal
// for both `queue`'s and `queue-start`'s identical accepted-vs-engaged gap
// (docs/error_model_v2.md §2): a Companion bind can 200 a command the Plex
// app never acts on. `staged` reports whether the persisted state entry that
// would let `queue-start` recover is actually there.
func playbackNotStartedErr(staged bool) *output.CLIError {
	return output.Err(output.CodePlaybackNotStarted, "device accepted the playback command but playback never started").
		WithHint("relaunch Plex on the client; recover with queue-start (staged) or re-run queue (not staged)").
		WithData("staged", staged).
		WithData("clientEngaged", false)
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
		q, cliErr := queue.Create(args, shuffle, repeat)
		if cliErr != nil {
			output.FailErr(cliErr)
			return nil
		}
		queueID := jsonx.AsStr(q["playQueueID"])
		selectedID := jsonx.AsStr(q["selectedItemID"])
		bind := playback.PlayQueue(target, queueID, selectedID)
		if !jsonx.Truthy(bind["ok"]) {
			// The queue exists on the server the moment Create succeeded.
			// StageBindFailure surfaces its IDs (in data) so a bind failure
			// leaves a queue the caller can see and recover, not an orphan —
			// resolved into CONFLICT vs STAGED per docs/error_model_v2.md §5.
			bindErr, _ := bind["error"].(string)
			output.FailErr(queue.StageBindFailure(target, bindErr, queueID, selectedID))
			return nil
		}
		// An accepted bind proves the device answered, not that the app
		// engaged (the Companion listener can 200 commands the app never
		// acts on). Confirm against sessions BEFORE deciding how to persist
		// state — mirrors v1's ordering exactly: only a bind AND a verified
		// engagement counts as ok:true, so only that combination gets the
		// unconditional Save overwrite. A bind that's accepted but never
		// engaged is NOT a verified success and stages exactly like a bind
		// failure (queue.Stage), never clobbering an existing entry.
		// Without --shuffle the engaged item must be the first key; with
		// it, whichever queued key PMS picked counts.
		expected := args[:1]
		if shuffle {
			expected = args
		}
		if !queue.ConfirmEngaged(target, expected) {
			outcome := queue.Stage(target, queueID, selectedID)
			if outcome.WriteErr != nil {
				output.FailErr(output.Err(output.CodeInternal,
					"device accepted the playback command but playback never started; additionally failed to stage queue state: "+outcome.WriteErr.Error()).
					WithHint("report this — plexctl bug").
					WithData("playQueueID", queueID).WithData("selectedItemID", selectedID).
					WithData("clientEngaged", false))
				return nil
			}
			output.FailErr(playbackNotStartedErr(outcome.Staged))
			return nil
		}
		// Verified success: this IS the live queue, record it unconditionally.
		// D2 ruling: a failed write here does NOT become a failure envelope —
		// playback is already running, so escalating to ok:false would be a
		// worse lie than the one W10 exists to remove. It surfaces instead as
		// a success-side warning (CodeStateSaveFailed).
		mid := ""
		if v := target["machineIdentifier"]; jsonx.Truthy(v) {
			mid = jsonx.AsStr(v)
		}
		stateSaved := true
		if mid != "" {
			if serr := queuestate.Save(mid, queueID, selectedID); serr != nil {
				stateSaved = false
			}
		}
		result := jsonx.J{
			"ok":             true,
			"playQueueID":    queueID,
			"selectedItemID": selectedID,
			"clientEngaged":  true,
		}
		if !stateSaved {
			output.Warn(result, output.CodeStateSaveFailed, "queue state file not written",
				"state file may be stale — a later queue-show can read empty")
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
		target := clients.Resolve(*client)
		result, cliErr := queue.Start(target)
		if cliErr != nil {
			output.FailErr(cliErr)
			return nil
		}
		// Same accepted-vs-engaged gap as queue. The queue's own items
		// (fetched best-effort) scope the match; a fetch failure degrades to
		// any non-idle session on the client. queue.Start never touches
		// persisted state on success, so the entry it just bound from is
		// still there — staged is unconditionally true for the recovery hint.
		if queue.ConfirmEngaged(target, queue.ItemRatingKeys(jsonx.AsStr(result["playQueueID"]))) {
			result["clientEngaged"] = true
			output.Out(result)
			return nil
		}
		output.FailErr(playbackNotStartedErr(true))
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
		result, cliErr := queue.Show(clients.Resolve(*client))
		if cliErr != nil {
			output.FailErr(cliErr)
			return nil
		}
		output.Out(result)
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
	addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		// Refused outright — no client resolution, no HTTP at all. PMS 1.43
		// 404s this endpoint; the binary now owns the refusal instead of
		// forwarding a misleading 404 (docs/error_model_v2.md §3).
		output.FailErr(queue.Shuffle())
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
	addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		output.FailErr(queue.Unshuffle())
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
		result, cliErr := queue.Clear(clients.Resolve(*client))
		if cliErr != nil {
			output.FailErr(cliErr)
			return nil
		}
		output.Out(result)
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
		result, cliErr := queue.RemoveItem(clients.Resolve(*client), args[0])
		if cliErr != nil {
			output.FailErr(cliErr)
			return nil
		}
		output.Out(result)
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
		result, cliErr := queue.AddToClient(clients.Resolve(*client), args)
		if cliErr != nil {
			output.FailErr(cliErr)
			return nil
		}
		output.Out(result)
		return nil
	}
	return cmd
}
