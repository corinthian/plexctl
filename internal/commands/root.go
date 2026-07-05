// Package commands wires the flat plexctl command surface onto cobra.
// Domain command files live in this package and self-register via init() +
// Register, so no shared file is edited when a domain lands.
package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/output"
)

var timeoutFlag float64

// Root mirrors the click group: JSON-only output, --timeout global option.
var Root = &cobra.Command{
	Use:     "plexctl",
	Version: api.Version,
	Short:   "Plex Media Server control CLI — output is JSON, designed for LLM consumption.",
	Long: `Plex Media Server control CLI — output is JSON, designed for LLM consumption.

All commands emit a JSON object with an "ok" boolean. On failure, "error"
contains a human-readable message. Exit code is 1 on failure, 2 when the
failure was a request timeout.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if cmd.Root().PersistentFlags().Changed("timeout") {
			api.SetTimeoutOverride(timeoutFlag)
		}
	},
}

var registrars []func(*cobra.Command)

// Register queues a domain's command constructor; Execute applies them all.
func Register(f func(*cobra.Command)) {
	registrars = append(registrars, f)
}

// Execute runs the CLI. Argument/usage errors exit 2 (click's UsageError
// convention, which the Python CLI inherited); domain failures exit via
// output.Out's 1/2 discipline before cobra ever sees an error.
func Execute() {
	Root.PersistentFlags().Float64Var(&timeoutFlag, "timeout", 0,
		"HTTP timeout in seconds (overrides $PLEXCTL_TIMEOUT and config `timeout`; default 10)")
	for _, f := range registrars {
		f(Root)
	}
	if err := Root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error: "+err.Error())
		output.Exit(2)
	}
}
