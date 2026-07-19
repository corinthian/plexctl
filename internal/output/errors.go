package output

import (
	"github.com/corinthian/plexctl/internal/jsonx"
)

// v2 error contract (docs/error_model_v2.md): every failure carries a code
// from the closed enumeration below, the exit class is derived from the code
// so call sites can never pair them wrong, and the error envelope is
// structured — {ok:false, error:{code,message,http_status?,hint?}, data?}.
// Errors print to stdout, matching traktctl's actual behavior (its Err writer
// is an unused seam).

// Exit classes (v2). Exit 64 is dead — usage errors are ExitUser.
const (
	ExitOK          = 0 // success
	ExitUser        = 1 // bad flags/args/invocation (BAD_REQUEST)
	ExitPlex        = 2 // Plex refused or errored (domain failures, HTTP 4xx/5xx semantics)
	ExitTransport   = 3 // timeout, connection failure, unreachable client/cloud
	ExitInternal    = 4 // plexctl bug
	ExitAuthMissing = 5 // not authenticated
	ExitNotApplied  = 6 // upstream 2xx but verification shows nothing changed
)

// Error codes. Closed set — a code not in codeExit is emitted as INTERNAL.
const (
	CodeBadRequest         = "BAD_REQUEST"
	CodeAuthRequired       = "PLEX_AUTH_REQUIRED"
	CodeAuthFailed         = "PLEX_AUTH_FAILED"
	CodeNothingPlaying     = "PLEX_NOTHING_PLAYING"
	CodeNotFound           = "PLEX_NOT_FOUND"
	CodeAllWatched         = "PLEX_ALL_WATCHED"
	CodeShowAmbiguous      = "PLEX_SHOW_AMBIGUOUS"
	CodeScopeRequired      = "PLEX_SCOPE_REQUIRED"
	CodeTrackNotFound      = "PLEX_TRACK_NOT_FOUND"
	CodeSeekFailed         = "PLEX_SEEK_FAILED"
	CodeClientUnknown      = "PLEX_CLIENT_UNKNOWN"
	CodeClientInactive     = "PLEX_CLIENT_INACTIVE"
	CodeClientAmbiguous    = "PLEX_CLIENT_AMBIGUOUS"
	CodeClientUnreachable  = "PLEX_CLIENT_UNREACHABLE"
	CodeCloudUnreachable   = "CLOUD_UNREACHABLE"
	CodeTransportTimeout   = "TRANSPORT_TIMEOUT"
	CodeTransportFailed    = "TRANSPORT_FAILED"
	CodeServerError        = "PLEX_SERVER_ERROR"
	CodeHTTPError          = "PLEX_HTTP_ERROR"
	CodeQueueCreateFailed  = "PLEX_QUEUE_CREATE_FAILED"
	CodeQueueStaged        = "PLEX_QUEUE_STAGED"
	CodeQueueConflict      = "PLEX_QUEUE_CONFLICT"
	CodePlaybackNotStarted = "PLEX_PLAYBACK_NOT_STARTED"
	CodeNoQueue            = "PLEX_NO_QUEUE"
	CodeQueuePartial       = "PLEX_QUEUE_PARTIAL"
	CodeSmartContainer     = "PLEX_SMART_CONTAINER"
	CodeUnsupported        = "PLEX_UNSUPPORTED"
	CodeStateSaveFailed    = "PLEX_STATE_SAVE_FAILED" // warning-only: never a failure envelope
	CodeNotApplied         = "NOT_APPLIED"
	CodeInternal           = "INTERNAL"
)

// codeExit derives the exit class from the code — the only place the pairing
// exists. CodeStateSaveFailed is deliberately absent: it is warning-only, and
// emitting it as a failure is a bug that lands on ExitInternal.
var codeExit = map[string]int{
	CodeBadRequest:         ExitUser,
	CodeAuthRequired:       ExitAuthMissing,
	CodeAuthFailed:         ExitPlex,
	CodeNothingPlaying:     ExitPlex,
	CodeNotFound:           ExitPlex,
	CodeAllWatched:         ExitPlex,
	CodeShowAmbiguous:      ExitPlex,
	CodeScopeRequired:      ExitPlex,
	CodeTrackNotFound:      ExitPlex,
	CodeSeekFailed:         ExitPlex,
	CodeClientUnknown:      ExitPlex,
	CodeClientInactive:     ExitPlex,
	CodeClientAmbiguous:    ExitPlex,
	CodeClientUnreachable:  ExitTransport,
	CodeCloudUnreachable:   ExitTransport,
	CodeTransportTimeout:   ExitTransport,
	CodeTransportFailed:    ExitTransport,
	CodeServerError:        ExitPlex,
	CodeHTTPError:          ExitPlex,
	CodeQueueCreateFailed:  ExitPlex,
	CodeQueueStaged:        ExitPlex,
	CodeQueueConflict:      ExitPlex,
	CodePlaybackNotStarted: ExitPlex,
	CodeNoQueue:            ExitPlex,
	CodeQueuePartial:       ExitPlex,
	CodeSmartContainer:     ExitPlex,
	CodeUnsupported:        ExitPlex,
	CodeNotApplied:         ExitNotApplied,
	CodeInternal:           ExitInternal,
}

// CLIError is the one failure type. It satisfies error so lower packages can
// return it up to a command layer that forwards to FailErr.
type CLIError struct {
	Code       string
	Message    string
	HTTPStatus int
	Hint       string
	Data       jsonx.J
}

func (e *CLIError) Error() string { return e.Message }

// Err builds a CLIError for a known code.
func Err(code, message string) *CLIError {
	return &CLIError{Code: code, Message: message}
}

// WithHint attaches the concrete next action (names the exact command).
func (e *CLIError) WithHint(hint string) *CLIError {
	e.Hint = hint
	return e
}

// WithHTTPStatus records the upstream status that drove the failure.
func (e *CLIError) WithHTTPStatus(status int) *CLIError {
	e.HTTPStatus = status
	return e
}

// WithData attaches a machine-usable recovery/context field.
func (e *CLIError) WithData(key string, value any) *CLIError {
	if e.Data == nil {
		e.Data = jsonx.J{}
	}
	e.Data[key] = value
	return e
}

// ExitCode resolves the exit class for this error's code; an unknown or
// warning-only code is a plexctl bug and resolves to ExitInternal.
func (e *CLIError) ExitCode() int {
	if code, ok := codeExit[e.Code]; ok {
		return code
	}
	return ExitInternal
}

// Envelope renders the v2 error envelope (without printing).
func (e *CLIError) Envelope() jsonx.J {
	body := jsonx.J{"code": e.Code, "message": e.Message}
	if _, known := codeExit[e.Code]; !known {
		body["code"] = CodeInternal
		body["message"] = "unknown error code " + e.Code + ": " + e.Message
	}
	if e.HTTPStatus != 0 {
		body["http_status"] = e.HTTPStatus
	}
	if e.Hint != "" {
		body["hint"] = e.Hint
	}
	env := jsonx.J{"ok": false, "error": body}
	if len(e.Data) > 0 {
		env["data"] = e.Data
	}
	return env
}

// FailErr prints the v2 error envelope and exits per the code's class.
func FailErr(e *CLIError) {
	Print(e.Envelope())
	Exit(e.ExitCode())
}

// Warn appends an error-shaped warning to a success result ("warnings" array).
// For non-fatal infrastructure failures that must not read as command failure
// (sole v2 producer: CodeStateSaveFailed).
func Warn(result jsonx.J, code, message, hint string) jsonx.J {
	w := jsonx.J{"code": code, "message": message}
	if hint != "" {
		w["hint"] = hint
	}
	warnings, _ := result["warnings"].([]jsonx.J)
	result["warnings"] = append(warnings, w)
	return result
}
