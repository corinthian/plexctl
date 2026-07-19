package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/clients"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/playback"
)

func init() {
	Register(func(root *cobra.Command) {
		root.AddCommand(
			newPlayCmd(),
			newPauseCmd(),
			newStopCmd(),
			newNextCmd(),
			newPrevCmd(),
			newSeekCmd(),
			newVolumeCmd(),
		)
	})
}

func newPlayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "play",
		Short: "Resume playback.",
		Args:  cobra.NoArgs,
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		result, cliErr := playback.Play(clients.Resolve(*client))
		if cliErr != nil {
			output.FailErr(cliErr)
			return nil
		}
		output.Out(result)
		return nil
	}
	return cmd
}

func newPauseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pause",
		Short: "Pause playback.",
		Args:  cobra.NoArgs,
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		result, cliErr := playback.Pause(clients.Resolve(*client))
		if cliErr != nil {
			output.FailErr(cliErr)
			return nil
		}
		output.Out(result)
		return nil
	}
	return cmd
}

func newStopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop playback.",
		Args:  cobra.NoArgs,
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		result, cliErr := playback.Stop(clients.Resolve(*client))
		if cliErr != nil {
			output.FailErr(cliErr)
			return nil
		}
		output.Out(result)
		return nil
	}
	return cmd
}

func newNextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "next",
		Short: "Step forward (next chapter / skip).",
		Args:  cobra.NoArgs,
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		result, cliErr := playback.StepForward(clients.Resolve(*client))
		if cliErr != nil {
			output.FailErr(cliErr)
			return nil
		}
		output.Out(result)
		return nil
	}
	return cmd
}

func newPrevCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prev",
		Short: "Step back.",
		Args:  cobra.NoArgs,
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		result, cliErr := playback.StepBack(clients.Resolve(*client))
		if cliErr != nil {
			output.FailErr(cliErr)
			return nil
		}
		output.Out(result)
		return nil
	}
	return cmd
}

// newSeekCmd mirrors cli.py's seek command, which sets
// ignore_unknown_options + allow_extra_args and takes POSITION as
// nargs=-1/UNPROCESSED so tokens like "-30s" survive click's option parser.
// Cobra has no direct equivalent, so flag parsing is disabled entirely and
// only a fixed set of flags (-c/--client, --no-unpause, --help, --timeout —
// the last ruled on 2026-07-10, ordinarily a root persistent flag click
// only accepted at the group level) are hand-parsed out of the raw args.
// Everything else joins POSITION exactly as click would pass it through,
// including -h: click never bound it here, and this port doesn't either
// (ruled 2026-07-10). This is not a general dash-letter-vs-dash-digit
// classifier — it's an enumerated extractor. A leading "-" followed by a
// digit is always a position (-30s, -1m); a leading "-" followed by a
// letter is a position UNLESS it's one of the flags enumerated above, in
// which case it's a flag. A bare "--" ends flag recognition for the rest
// of the args.
func newSeekCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "seek POSITION",
		Short: "Seek to POSITION in the current media.",
		Long: `Seek to POSITION in the current media.

POSITION formats: absolute mm:ss (e.g. 1:30), relative +Ns or -Ns (e.g. +30s, -1m).

Flag parsing is hand-rolled so position formats like -30s survive: only
-c/--client, --no-unpause, --help, and --timeout <seconds>/--timeout=<seconds>
are recognized as flags. -h is not bound and joins POSITION like any other
unrecognized token. Use "--" to force everything after it into POSITION.`,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var client string
			noUnpause := false
			var positionParts []string
			flagsDone := false

			for i := 0; i < len(args); i++ {
				a := args[i]
				if flagsDone {
					positionParts = append(positionParts, a)
					continue
				}
				switch {
				case a == "--help":
					return cmd.Help()
				case a == "--":
					flagsDone = true
				case a == "-c" || a == "--client":
					i++
					if i >= len(args) {
						return fmt.Errorf("flag needs an argument: %s", a)
					}
					client = args[i]
				case strings.HasPrefix(a, "--client="):
					client = strings.TrimPrefix(a, "--client=")
				case a == "--no-unpause":
					noUnpause = true
				case a == "--timeout":
					i++
					if i >= len(args) {
						return fmt.Errorf("flag needs an argument: %s", a)
					}
					if err := setSeekTimeoutOverride(args[i]); err != nil {
						return err
					}
				case strings.HasPrefix(a, "--timeout="):
					if err := setSeekTimeoutOverride(strings.TrimPrefix(a, "--timeout=")); err != nil {
						return err
					}
				default:
					positionParts = append(positionParts, a)
				}
			}

			if len(positionParts) == 0 {
				return fmt.Errorf("POSITION required")
			}
			position := strings.Join(positionParts, " ")
			result, cliErr := playback.Seek(clients.Resolve(client), position, !noUnpause)
			if cliErr != nil {
				output.FailErr(cliErr)
				return nil
			}
			output.Out(result)
			return nil
		},
	}
	return cmd
}

// setSeekTimeoutOverride applies seek's hand-parsed --timeout exactly as
// root.go's PersistentPreRunE would for every other command, including the
// W1 non-positive rejection — seek's DisableFlagParsing means root's own
// boundary check never runs for this command, so this is the only place
// that guards against reproducing the --timeout 0 hang here.
func setSeekTimeoutOverride(raw string) error {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fmt.Errorf("invalid value for '--timeout': '%s' is not a valid float", raw)
	}
	if v <= 0 {
		return fmt.Errorf("invalid value for '--timeout': %v is not greater than 0", v)
	}
	api.SetTimeoutOverride(v)
	return nil
}

// newVolumeCmd is an unconditional refusal (v2, docs/error_model_v2.md §2
// CodeUnsupported row): the Apple TV Companion listener accepts a volume
// setParameters command and silently ignores it, so v1's "success" was
// theater. v2 absorbs the skill's ban directly into the binary — no client
// resolution, no Companion round trip, just CodeUnsupported at exit 2 after
// LEVEL's own range validation (which stays, so a malformed LEVEL is still a
// usage error rather than being masked by the refusal).
func newVolumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume LEVEL",
		Short: "Set volume to LEVEL (integer 0-100).",
		Args:  cobra.ExactArgs(1),
	}
	_ = addClientFlag(cmd) // kept for CLI/flag compatibility; unused — no client is ever resolved
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		level, err := strconv.Atoi(args[0])
		if err != nil || level < 0 || level > 100 {
			return fmt.Errorf("invalid value for 'LEVEL': '%s' is not in the range 0<=x<=100", args[0])
		}
		output.FailErr(output.Err(output.CodeUnsupported, "volume control is not supported — the client ignores Companion volume commands"))
		return nil
	}
	return cmd
}
