package commands

import (
	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/app"
	"github.com/corinthian/plexctl/internal/auth"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/output"
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
	cmd.AddCommand(newAuthRegisterCmd())
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

func newAuthRegisterCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "register",
		Short: "Refresh this client's plex.tv device record with the current version.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			token := app.Current().Require("token")
			clientID := api.ClientID()
			if err := api.RegisterDevice(token, clientID); err != nil {
				e := api.AsError(err)
				cli := api.Classify(e, api.TargetCloud)
				cli.Message = "plex.tv " + e.Message
				output.FailErr(cli)
				return nil
			}
			output.PrintOrFail(jsonx.J{"ok": true, "clientId": clientID, "version": api.Version})
			return nil
		},
	}
}
