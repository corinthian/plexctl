package commands

import (
	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/auth"
)

func init() {
	Register(func(root *cobra.Command) {
		root.AddCommand(newAuthCmd())
	})
}

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authentication commands.",
	}
	cmd.AddCommand(newAuthLoginCmd())
	return cmd
}

func newAuthLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "One-time login — saves token to ~/.config/plexctl/config.toml.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			auth.Login()
			return nil
		},
	}
}
