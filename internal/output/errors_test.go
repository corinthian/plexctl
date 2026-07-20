package output

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/corinthian/plexctl/internal/jsonx"
)

// capture swaps the Stdout/Exit seams, runs f, and returns printed output +
// recorded exit code (-1 when Exit was never called).
func capture(t *testing.T, f func()) (string, int) {
	t.Helper()
	var buf bytes.Buffer
	exit := -1
	oldOut, oldExit := Stdout, Exit
	Stdout, Exit = &buf, func(code int) { exit = code }
	defer func() { Stdout, Exit = oldOut, oldExit }()
	f()
	return buf.String(), exit
}

func TestCodeExitPairs(t *testing.T) {
	cases := map[string]int{
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
	for code, want := range cases {
		if got := Err(code, "m").ExitCode(); got != want {
			t.Errorf("%s: exit %d, want %d", code, got, want)
		}
	}
	if len(cases) != len(codeExit) {
		t.Errorf("test covers %d codes, codeExit has %d — keep them in lockstep", len(cases), len(codeExit))
	}
}

func TestFailErrEnvelope(t *testing.T) {
	out, exit := capture(t, func() {
		FailErr(Err(CodeQueueStaged, "made the queue but the client did not respond").
			WithHint("run: plexctl queue-start once the client is awake").
			WithData("staged", true).
			WithData("playQueueID", "123"))
	})
	if exit != ExitPlex {
		t.Fatalf("exit = %d, want %d", exit, ExitPlex)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, out)
	}
	if env["ok"] != false {
		t.Errorf("ok = %v, want false", env["ok"])
	}
	errObj := env["error"].(map[string]any)
	if errObj["code"] != CodeQueueStaged {
		t.Errorf("code = %v", errObj["code"])
	}
	if errObj["hint"] == "" || errObj["hint"] == nil {
		t.Error("hint missing")
	}
	if _, present := errObj["http_status"]; present {
		t.Error("http_status must be omitted when zero")
	}
	data := env["data"].(map[string]any)
	if data["staged"] != true || data["playQueueID"] != "123" {
		t.Errorf("data = %v", data)
	}
}

func TestFailErrHTTPStatus(t *testing.T) {
	out, exit := capture(t, func() {
		FailErr(Err(CodeServerError, "PMS 500").WithHTTPStatus(500))
	})
	if exit != ExitPlex {
		t.Fatalf("exit = %d", exit)
	}
	var env map[string]any
	_ = json.Unmarshal([]byte(out), &env)
	errObj := env["error"].(map[string]any)
	if errObj["http_status"] != float64(500) {
		t.Errorf("http_status = %v", errObj["http_status"])
	}
	if _, present := env["data"]; present {
		t.Error("data must be omitted when empty")
	}
}

func TestUnknownCodeBecomesInternal(t *testing.T) {
	out, exit := capture(t, func() {
		FailErr(Err("PLEX_MADE_UP", "oops"))
	})
	if exit != ExitInternal {
		t.Fatalf("exit = %d, want %d", exit, ExitInternal)
	}
	var env map[string]any
	_ = json.Unmarshal([]byte(out), &env)
	errObj := env["error"].(map[string]any)
	if errObj["code"] != CodeInternal {
		t.Errorf("code = %v, want %s", errObj["code"], CodeInternal)
	}
}

func TestWarningOnlyCodeIsNotAFailureCode(t *testing.T) {
	// CodeStateSaveFailed must not be emittable as a failure class.
	if _, ok := codeExit[CodeStateSaveFailed]; ok {
		t.Fatal("CodeStateSaveFailed must be absent from codeExit (warning-only)")
	}
	if got := Err(CodeStateSaveFailed, "m").ExitCode(); got != ExitInternal {
		t.Errorf("exit = %d, want ExitInternal", got)
	}
}

func TestWarnAppends(t *testing.T) {
	result := jsonx.J{"ok": true}
	result = Warn(result, CodeStateSaveFailed, "state file not written", "queue-show may read empty")
	result = Warn(result, CodeStateSaveFailed, "second", "")
	warnings := result["warnings"].([]jsonx.J)
	if len(warnings) != 2 {
		t.Fatalf("warnings = %d, want 2", len(warnings))
	}
	if warnings[0]["code"] != CodeStateSaveFailed || warnings[0]["hint"] == nil {
		t.Errorf("first warning malformed: %v", warnings[0])
	}
	if _, present := warnings[1]["hint"]; present {
		t.Error("empty hint must be omitted")
	}
}

func TestCLIErrorSatisfiesError(t *testing.T) {
	var err error = Err(CodeNotFound, "nothing found for: rocky")
	if err.Error() != "nothing found for: rocky" {
		t.Errorf("Error() = %q", err.Error())
	}
}
