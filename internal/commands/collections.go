package commands

import (
	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/collections"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/output"
)

func init() {
	Register(func(root *cobra.Command) {
		root.AddCommand(newCollectionCmd())
	})
}

func newCollectionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "collection",
		Short: "Browse library collections.",
	}
	cmd.AddCommand(
		newCollectionListCmd(),
		newCollectionShowCmd(),
		newCollectionCreateCmd(),
		newCollectionDeleteCmd(),
		newCollectionRenameCmd(),
		newCollectionAddCmd(),
		newCollectionRemoveCmd(),
	)
	return cmd
}

func newCollectionListCmd() *cobra.Command {
	var section string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List collections, in a given section or across all video sections.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			items := collections.ListAll(section)
			output.Out(jsonx.J{"ok": true, "count": len(items), "collections": items})
			return nil
		},
	}
	cmd.Flags().StringVarP(&section, "section", "s", "", "Section ID (omit to list across all video sections)")
	return cmd
}

func newCollectionShowCmd() *cobra.Command {
	var raw bool
	cmd := &cobra.Command{
		Use:   "show RATING_KEY",
		Short: "List the items in a collection by RATING_KEY.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			items := collections.Show(args[0], raw)
			output.Out(jsonx.J{"ok": true, "count": len(items), "items": items})
			return nil
		},
	}
	cmd.Flags().BoolVar(&raw, "raw", false,
		"Bypass smart-collection resolution and return PMS's raw /children payload — useful only for debugging.")
	return cmd
}

func newCollectionCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create TITLE SECTION_ID RATING_KEYS...",
		Short: "Create a manual collection in SECTION_ID titled TITLE, seeded with one or more RATING_KEYS.",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, cliErr := collections.Create(args[0], args[1], args[2:])
			if cliErr != nil {
				output.FailErr(cliErr)
				return nil
			}
			output.Out(result)
			return nil
		},
	}
}

func newCollectionDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete RATING_KEY",
		Short: "Delete a collection by RATING_KEY.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			output.Out(collections.Delete(args[0]))
			return nil
		},
	}
}

func newCollectionRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename RATING_KEY NEW_TITLE",
		Short: "Rename a collection to NEW_TITLE.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, cliErr := collections.Rename(args[0], args[1])
			if cliErr != nil {
				output.FailErr(cliErr)
				return nil
			}
			output.Out(result)
			return nil
		},
	}
}

func newCollectionAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add COLLECTION_KEY RATING_KEYS...",
		Short: "Add one or more RATING_KEYS to the collection identified by COLLECTION_KEY.",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, cliErr := collections.AddItems(args[0], args[1:])
			if cliErr != nil {
				output.FailErr(cliErr)
				return nil
			}
			output.Out(result)
			return nil
		},
	}
}

func newCollectionRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove COLLECTION_KEY ITEM_RATING_KEY",
		Short: "Remove ITEM_RATING_KEY from the collection identified by COLLECTION_KEY.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, cliErr := collections.RemoveItem(args[0], args[1])
			if cliErr != nil {
				output.FailErr(cliErr)
				return nil
			}
			output.Out(result)
			return nil
		},
	}
}
