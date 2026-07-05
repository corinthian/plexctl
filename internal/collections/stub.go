// Package collections ports plexctl/collections.py: list/show with smart
// resolution, and manual-collection mutation with the smart guard, DELETE
// remove quirk, and title.locked rename.
package collections

import "github.com/corinthian/plexctl/internal/jsonx"

// ListAll mirrors collections.list_all. sectionID == "" means every video
// section.
func ListAll(sectionID string) []jsonx.J { panic("not ported: collections.ListAll") }

// Show mirrors collections.show (smart-filter resolution unless raw).
func Show(ratingKey string, raw bool) []jsonx.J { panic("not ported: collections.Show") }

// Create / Delete / Rename / AddItems / RemoveItem mirror their namesakes.
func Create(title, sectionID string, ratingKeys []string) jsonx.J {
	panic("not ported: collections.Create")
}
func Delete(collectionKey string) jsonx.J { panic("not ported: collections.Delete") }
func Rename(collectionKey, newTitle string) jsonx.J {
	panic("not ported: collections.Rename")
}
func AddItems(collectionKey string, ratingKeys []string) jsonx.J {
	panic("not ported: collections.AddItems")
}
func RemoveItem(collectionKey, itemRatingKey string) jsonx.J {
	panic("not ported: collections.RemoveItem")
}
