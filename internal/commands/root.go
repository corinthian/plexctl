// Package commands wires the flat plexctl command surface onto cobra.
// Domain command files live in this package and self-register via init() +
// Register, so no shared file is edited when a domain lands.
package commands

import (
	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/output"
)

var timeoutFlag string

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

All commands emit a JSON object with an "ok" boolean. On failure, the
envelope is {"ok": false, "error": {"code", "message", "http_status"?,
"hint"?}, "data"?} — "code" is a stable member of a closed enumeration
(never match on "message", which is free-text and unstable).

Exit codes: 0 success, 1 bad invocation (malformed command/flags/args —
never retry, fix the command), 2 Plex refused or errored the request,
3 transport failure (timeout, connection failure, unreachable client or
cloud), 4 internal plexctl bug, 5 not authenticated, 6 accepted but not
applied (upstream reported success but verification found nothing changed).

Run "plexctl commands" for a machine-readable JSON listing of every command
in this tree.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Resolved once, here, for every command. Any rejection —
			// whatever the source — is a BAD_REQUEST at exit 1: plexctl's
			// closed map has no config family, and routing a numeric typo
			// through PLEX_AUTH_REQUIRED would tell the user to run
			// auth login (contract 2.7, plexctl exception).
			d, err := api.ResolveTimeout(cmd.Root().PersistentFlags().Changed("timeout"), timeoutFlag)
			if err != nil {
				return err
			}
			api.SetTimeout(d)
			return nil
		},
	}
	// A StringVar, not an IntVar: cobra's own parse error for an int flag
	// would replace the source-named message contract 2.1 requires, and
	// `--timeout ""` would be rejected by an accident of cobra's parsing
	// rather than by the grammar.
	root.PersistentFlags().StringVar(&timeoutFlag, "timeout", "",
		"HTTP timeout, whole seconds 1-86400 (overrides $PLEXCTL_TIMEOUT and config `timeout`; default 10)")
	for _, f := range registrars {
		f(root)
	}
	return root
}

// Execute runs the CLI. Argument/usage errors that reach cobra as a RunE
// error — cobra's own arg-count/unknown-flag rejections, and every
// hand-rolled validator (choiceError, rate/volume/history-limit range
// checks) that returns an error instead of calling output directly — all
// become BAD_REQUEST at exit 1 (v2: exit 64 is dead). Domain failures exit
// via output.FailErr's coded discipline before cobra ever sees an error.
func Execute() {
	if err := BuildRoot().Execute(); err != nil {
		output.FailErr(output.Err(output.CodeBadRequest, err.Error()))
	}
}
