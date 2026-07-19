package commands

import (
	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/output"
)

func init() {
	Register(func(root *cobra.Command) {
		root.AddCommand(newCommandsCmd())
	})
}

// newCommandsCmd implements `plexctl commands`: machine-readable discovery
// of the whole command tree, so a caller (e.g. an LLM skill) never has to
// scrape --help text.
func newCommandsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "commands",
		Short: "Emit the full command tree as JSON (machine-readable discovery).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			output.Out(jsonx.J{"ok": true, "commands": commandNode(cmd.Root())})
			return nil
		},
	}
}

// commandNode walks the cobra tree rooted at cmd into the discovery shape —
// name/path/summary/subcommands — mirroring traktctl's nodeFor/commandNode
// (internal/commands/llm.go in that repo). Hidden commands, "help", and
// "completion" are skipped so the tree matches what a user can actually run.
func commandNode(cmd *cobra.Command) jsonx.J {
	node := jsonx.J{
		"name":    cmd.Name(),
		"path":    cmd.CommandPath(),
		"summary": cmd.Short,
	}
	var subcommands []jsonx.J
	for _, c := range cmd.Commands() {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		subcommands = append(subcommands, commandNode(c))
	}
	if len(subcommands) > 0 {
		node["subcommands"] = subcommands
	}
	return node
}
