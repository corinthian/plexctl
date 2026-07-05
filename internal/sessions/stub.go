// Package sessions ports plexctl/sessions.py: now-playing, history,
// continue-watching, and the parallel-fetched context bundle.
package sessions

import "github.com/corinthian/plexctl/internal/jsonx"

// NowPlaying mirrors sessions.now_playing (idle is ok:true, state idle).
func NowPlaying(client jsonx.J) jsonx.J { panic("not ported: sessions.NowPlaying") }

// CurrentRatingKey mirrors sessions.current_rating_key; "" when idle.
func CurrentRatingKey(client jsonx.J) string { panic("not ported: sessions.CurrentRatingKey") }

// History mirrors sessions.history.
func History(limit int) jsonx.J { panic("not ported: sessions.History") }

// Context mirrors sessions.context (goroutine-parallel fetch bundle).
func Context(client jsonx.J, historyLimit int, includeHistory bool) jsonx.J {
	panic("not ported: sessions.Context")
}

// ContinueWatching mirrors sessions.continue_watching.
func ContinueWatching() jsonx.J { panic("not ported: sessions.ContinueWatching") }
