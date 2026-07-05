package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/playlists"
)

func init() {
	Register(func(root *cobra.Command) {
		root.AddCommand(newPlaylistCmd())
	})
}

func validPlaylistType(t string) bool {
	switch t {
	case "video", "audio", "photo":
		return true
	}
	return false
}

func newPlaylistCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "playlist",
		Short: "Browse playlists.",
	}
	cmd.AddCommand(
		newPlaylistListCmd(),
		newPlaylistShowCmd(),
		newPlaylistCreateCmd(),
		newPlaylistDeleteCmd(),
		newPlaylistRenameCmd(),
		newPlaylistAddCmd(),
		newPlaylistRemoveCmd(),
		newPlaylistClearCmd(),
	)
	return cmd
}

func newPlaylistListCmd() *cobra.Command {
	var playlistType string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all playlists, optionally filtered by type.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := choiceError(cmd, "type", playlistType, "video", "audio", "photo"); err != nil {
				return err
			}
			items, err := playlists.ListAll(playlistType)
			if err != nil {
				output.Out(jsonx.J{"ok": false, "error": err.Error()})
				return nil
			}
			output.Out(jsonx.J{"ok": true, "count": len(items), "playlists": items})
			return nil
		},
	}
	cmd.Flags().StringVar(&playlistType, "type", "", "Filter by playlist type")
	return cmd
}

func newPlaylistShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show RATING_KEY",
		Short: "List items in a playlist by RATING_KEY.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			items := playlists.Show(args[0])
			output.Out(jsonx.J{"ok": true, "count": len(items), "items": items})
			return nil
		},
	}
}

func newPlaylistCreateCmd() *cobra.Command {
	var playlistType string
	cmd := &cobra.Command{
		Use:   "create TITLE RATING_KEYS...",
		Short: "Create a manual playlist titled TITLE, seeded with one or more RATING_KEYS.",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !validPlaylistType(playlistType) {
				return fmt.Errorf("invalid value for '--type': '%s' is not one of 'video', 'audio', 'photo'", playlistType)
			}
			output.Out(playlists.Create(args[0], playlistType, args[1:]))
			return nil
		},
	}
	cmd.Flags().StringVar(&playlistType, "type", "video", "Playlist type (default: video)")
	return cmd
}

func newPlaylistDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete RATING_KEY",
		Short: "Delete a playlist by RATING_KEY.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			output.Out(playlists.Delete(args[0]))
			return nil
		},
	}
}

func newPlaylistRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename RATING_KEY NEW_TITLE",
		Short: "Rename a playlist to NEW_TITLE.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			output.Out(playlists.Rename(args[0], args[1]))
			return nil
		},
	}
}

func newPlaylistAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add PLAYLIST_KEY RATING_KEYS...",
		Short: "Add one or more RATING_KEYS to the playlist identified by PLAYLIST_KEY.",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			output.Out(playlists.AddItems(args[0], args[1:]))
			return nil
		},
	}
}

func newPlaylistRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove PLAYLIST_KEY ITEM_ID",
		Short: "Remove ITEM_ID (playlistItemID from `playlist show`) from PLAYLIST_KEY.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			output.Out(playlists.RemoveItem(args[0], args[1]))
			return nil
		},
	}
}

func newPlaylistClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear PLAYLIST_KEY",
		Short: "Remove every item from PLAYLIST_KEY (playlist itself is preserved).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			output.Out(playlists.Clear(args[0]))
			return nil
		},
	}
}
