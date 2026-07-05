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
		output.Out(playback.Play(clients.Resolve(*client)))
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
		output.Out(playback.Pause(clients.Resolve(*client)))
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
		output.Out(playback.Stop(clients.Resolve(*client)))
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
		output.Out(playback.StepForward(clients.Resolve(*client)))
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
		output.Out(playback.StepBack(clients.Resolve(*client)))
		return nil
	}
	return cmd
}

// newSeekCmd mirrors cli.py's seek command, which sets
// ignore_unknown_options + allow_extra_args and takes POSITION as
// nargs=-1/UNPROCESSED so tokens like "-30s" survive click's option parser.
// Cobra has no direct equivalent, so flag parsing is disabled entirely and
// the recognized flags (-c/--client, --no-unpause, --timeout) are hand-parsed
// out of the raw args; everything else joins into POSITION.
func newSeekCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "seek POSITION",
		Short: "Seek to POSITION in the current media.",
		Long: `Seek to POSITION in the current media.

POSITION formats: absolute mm:ss (e.g. 1:30), relative +Ns or -Ns (e.g. +30s, -1m).`,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var client string
			noUnpause := false
			var positionParts []string

			for i := 0; i < len(args); i++ {
				a := args[i]
				switch {
				case a == "-h" || a == "--help":
					return cmd.Help()
				case a == "--":
					// bare separator — skip
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
						return fmt.Errorf("flag needs an argument: --timeout")
					}
					t, err := strconv.ParseFloat(args[i], 64)
					if err != nil {
						return fmt.Errorf("invalid --timeout value: %s", args[i])
					}
					api.SetTimeoutOverride(t)
				case strings.HasPrefix(a, "--timeout="):
					raw := strings.TrimPrefix(a, "--timeout=")
					t, err := strconv.ParseFloat(raw, 64)
					if err != nil {
						return fmt.Errorf("invalid --timeout value: %s", raw)
					}
					api.SetTimeoutOverride(t)
				default:
					positionParts = append(positionParts, a)
				}
			}

			if len(positionParts) == 0 {
				return fmt.Errorf("POSITION required")
			}
			position := strings.Join(positionParts, " ")
			output.Out(playback.Seek(clients.Resolve(client), position, !noUnpause))
			return nil
		},
	}
	return cmd
}

func newVolumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume LEVEL",
		Short: "Set volume to LEVEL (integer 0-100).",
		Args:  cobra.ExactArgs(1),
	}
	client := addClientFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		level, err := strconv.Atoi(args[0])
		if err != nil || level < 0 || level > 100 {
			return fmt.Errorf("invalid value for 'LEVEL': '%s' is not in the range 0<=x<=100", args[0])
		}
		output.Out(playback.SetVolume(clients.Resolve(*client), level))
		return nil
	}
	return cmd
}
