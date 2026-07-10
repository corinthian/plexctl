// Package commands wires the flat plexctl command surface onto cobra.
// Domain command files live in this package and self-register via init() +
// Register, so no shared file is edited when a domain lands.
package commands

import (
	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/output"
)

var timeoutFlag float64

var registrars []func(*cobra.Command)

// Register queues a domain's command constructor; BuildRoot applies them all.
func Register(f func(*cobra.Command)) {
	registrars = append(registrars, f)
}

// BuildRoot constructs a fresh command tree (fresh flag state — tests build
// one per invocation; Execute builds one per process).
func BuildRoot() *cobra.Command {
	root := &cobra.Command{
		Use:     "plexctl",
		Version: api.Version,
		Short:   "Plex Media Server control CLI — output is JSON, designed for LLM consumption.",
		Long: `Plex Media Server control CLI — output is JSON, designed for LLM consumption.

All commands emit a JSON object with an "ok" boolean. On failure, "error"
contains a human-readable message. Exit code is 0 on success, 1 on a domain
failure, 2 when the failure was a request timeout, 64 on a usage or
validation error (malformed invocation — never retry, fix the command).`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if cmd.Root().PersistentFlags().Changed("timeout") {
				api.SetTimeoutOverride(timeoutFlag)
			}
		},
	}
	root.PersistentFlags().Float64Var(&timeoutFlag, "timeout", 0,
		"HTTP timeout in seconds (overrides $PLEXCTL_TIMEOUT and config `timeout`; default 10)")
	for _, f := range registrars {
		f(root)
	}
	return root
}

// Execute runs the CLI. Argument/usage errors that reach cobra as a RunE
// error — cobra's own arg-count/unknown-flag rejections, and every
// hand-rolled validator (choiceError, rate/volume/history-limit range
// checks) that returns an error instead of calling output directly — all
// exit 64 through output.Usage. Domain failures exit via output.Out's 1/2
// discipline before cobra ever sees an error.
func Execute() {
	if err := BuildRoot().Execute(); err != nil {
		output.Usage(err.Error())
	}
}
