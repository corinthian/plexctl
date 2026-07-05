package commands

import (
	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/clients"
)

func init() {
	Register(func(root *cobra.Command) {
		root.AddCommand(newClientsCmd())
	})
}

func newClientsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clients",
		Short: "List all registered clients; shows which are currently controllable.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			clients.PrintClients()
			return nil
		},
	}
}
