// Package playback ports plexctl/playback.py: direct-to-client Companion
// commands (:32500), the monotonic commandID, the seek pause dance, and
// playMedia/playQueue with the address/port params.
package playback

import "github.com/corinthian/plexctl/internal/jsonx"

// CompanionTransportError mirrors playback.CompanionTransportError.
type CompanionTransportError struct{ Msg string }

func (e *CompanionTransportError) Error() string { return e.Msg }

// PlayerGet mirrors playback._player_get: GET from the client's Companion
// endpoint; parsed JSON or CompanionTransportError.
func PlayerGet(client jsonx.J, path string, extraParams map[string]string) (jsonx.J, error) {
	panic("not ported: playback.PlayerGet")
}

// GetServerMachineID mirrors playback._get_server_machine_id; "" on failure.
func GetServerMachineID() string { panic("not ported: playback.GetServerMachineID") }

// Transport commands mirror their Python namesakes.
func Play(client jsonx.J) jsonx.J        { panic("not ported: playback.Play") }
func Pause(client jsonx.J) jsonx.J       { panic("not ported: playback.Pause") }
func Stop(client jsonx.J) jsonx.J        { panic("not ported: playback.Stop") }
func StepForward(client jsonx.J) jsonx.J { panic("not ported: playback.StepForward") }
func StepBack(client jsonx.J) jsonx.J    { panic("not ported: playback.StepBack") }

// SetVolume mirrors playback.set_volume.
func SetVolume(client jsonx.J, level int) jsonx.J { panic("not ported: playback.SetVolume") }

// Seek mirrors playback.seek including the paused-player resume→seek→re-pause
// dance and its 1s wait.
func Seek(client jsonx.J, position string, unpause bool) jsonx.J {
	panic("not ported: playback.Seek")
}

// PlayQueue mirrors playback.play_queue.
func PlayQueue(client jsonx.J, queueID, selectedItemID string) jsonx.J {
	panic("not ported: playback.PlayQueue")
}

// PlayMedia mirrors playback.play_media.
func PlayMedia(client jsonx.J, ratingKey string) jsonx.J {
	panic("not ported: playback.PlayMedia")
}
