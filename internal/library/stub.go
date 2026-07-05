// Package library ports plexctl/library.py: search, sections, section
// listing with the client-side show unwatched filter, metadata, episode
// enumeration, scrobble/rate.
package library

import "github.com/corinthian/plexctl/internal/jsonx"

// Search mirrors library.search. mediaType == "" means unfiltered.
func Search(query, mediaType string, minScore float64) []jsonx.J {
	panic("not ported: library.Search")
}

// ResolveShow mirrors library.resolve_show; nil when no hit.
func ResolveShow(showQuery string) jsonx.J { panic("not ported: library.ResolveShow") }

// ShowEpisodes mirrors library.show_episodes. season == nil means all seasons.
func ShowEpisodes(showQuery string, unwatchedOnly bool, season *int) []jsonx.J {
	panic("not ported: library.ShowEpisodes")
}

// EpisodesForShowKey mirrors library.episodes_for_show_key.
func EpisodesForShowKey(showKey any, unwatchedOnly bool, season *int) []jsonx.J {
	panic("not ported: library.EpisodesForShowKey")
}

// LatestUnwatchedEpisode mirrors library.latest_unwatched_episode; nil when
// no show matches (or strict and nothing unwatched).
func LatestUnwatchedEpisode(showQuery string, strict bool) jsonx.J {
	panic("not ported: library.LatestUnwatchedEpisode")
}

// Sections mirrors library.sections.
func Sections() []jsonx.J { panic("not ported: library.Sections") }

// ListSection mirrors library.list_section. mediaType/sort == "" mean unset.
func ListSection(sectionID, mediaType string, unwatched bool, sort string) []jsonx.J {
	panic("not ported: library.ListSection")
}

// Metadata mirrors library.metadata; empty J when not found.
func Metadata(ratingKey string) jsonx.J { panic("not ported: library.Metadata") }

// FillDurations mirrors library.fill_durations (parallel best-effort fill of
// duration/year on rows missing them). Mutates rows in place and returns them.
func FillDurations(rows []jsonx.J) []jsonx.J { panic("not ported: library.FillDurations") }

// Scrobble / Unscrobble / Rate mirror their Python namesakes.
func Scrobble(ratingKey string) jsonx.J   { panic("not ported: library.Scrobble") }
func Unscrobble(ratingKey string) jsonx.J { panic("not ported: library.Unscrobble") }
func Rate(ratingKey string, rating int) jsonx.J { panic("not ported: library.Rate") }
