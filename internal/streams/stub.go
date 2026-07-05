// Package streams ports plexctl/streams.py: the audio/subtitle domain —
// batched audit reads (chunked /library/metadata/{ids}) and track-selection
// writes (PUT /library/parts/{partId}), single + bulk with guards.
package streams

import (
	"iter"

	"github.com/corinthian/plexctl/internal/jsonx"
)

// AudioStreams mirrors streams.audio_streams (streamType==2 across Media→Part).
func AudioStreams(meta jsonx.J) []jsonx.J { panic("not ported: streams.AudioStreams") }

// IterAuditRows mirrors streams.iter_audit_rows: yields per-episode audit
// rows chunk-by-chunk (rows for a chunk appear as soon as its batched GET
// returns).
func IterAuditRows(episodes []jsonx.J, preferred string) iter.Seq[jsonx.J] {
	panic("not ported: streams.IterAuditRows")
}

// AuditAudioForKey mirrors streams.audit_audio_for_key (generator form).
func AuditAudioForKey(showKey any, preferred string, season *int) iter.Seq[jsonx.J] {
	panic("not ported: streams.AuditAudioForKey")
}

// SetAudioStream mirrors streams.set_audio_stream. streamID nil → resolve by
// language.
func SetAudioStream(ratingKey string, language string, streamID *int) jsonx.J {
	panic("not ported: streams.SetAudioStream")
}

// SetSubtitleStream mirrors streams.set_subtitle_stream. disable=true sends
// subtitleStreamID=0.
func SetSubtitleStream(ratingKey string, language string, streamID *int, disable bool) jsonx.J {
	panic("not ported: streams.SetSubtitleStream")
}

// PlanBulkAudio mirrors streams.plan_bulk_audio (one batched metadata read).
func PlanBulkAudio(episodes []jsonx.J, language string, onlyNonEng bool) []jsonx.J {
	panic("not ported: streams.PlanBulkAudio")
}

// ExecuteBulkAudio mirrors streams.execute_bulk_audio (per-item TryPut;
// tolerate per-item failure).
func ExecuteBulkAudio(plan []jsonx.J) []jsonx.J { panic("not ported: streams.ExecuteBulkAudio") }
