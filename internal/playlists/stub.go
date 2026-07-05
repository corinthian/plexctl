// Package playlists ports plexctl/playlists.py: account-level playlists with
// playlistItemID mutation handles and the smart guard.
package playlists

import "github.com/corinthian/plexctl/internal/jsonx"

// ListAll mirrors playlists.list_all; the error mirrors its ValueError on a
// bad type (the CLI converts it to the standard envelope).
func ListAll(playlistType string) ([]jsonx.J, error) {
	panic("not ported: playlists.ListAll")
}

// Show mirrors playlists.show.
func Show(ratingKey string) []jsonx.J { panic("not ported: playlists.Show") }

// Create / Delete / Rename / AddItems / RemoveItem / Clear mirror their
// namesakes.
func Create(title, playlistType string, ratingKeys []string) jsonx.J {
	panic("not ported: playlists.Create")
}
func Delete(playlistKey string) jsonx.J { panic("not ported: playlists.Delete") }
func Rename(playlistKey, newTitle string) jsonx.J {
	panic("not ported: playlists.Rename")
}
func AddItems(playlistKey string, ratingKeys []string) jsonx.J {
	panic("not ported: playlists.AddItems")
}
func RemoveItem(playlistKey, playlistItemID string) jsonx.J {
	panic("not ported: playlists.RemoveItem")
}
func Clear(playlistKey string) jsonx.J { panic("not ported: playlists.Clear") }
