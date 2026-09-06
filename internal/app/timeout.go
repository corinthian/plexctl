package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/corinthian/plexctl/internal/config"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/xduration"
)

// DefaultTimeout is the timeout used when no source supplies one.
const DefaultTimeout = 10 * time.Second

// timeoutForTest, when set, wins over every App's resolved value, and it is
// deliberately package-scoped rather than a field: root installs a fresh App
// on every invocation, so an override stored on one App would be discarded
// the moment a test ran a command through the root. Fourteen call sites force
// a sub-second timeout this way and clear it with t.Cleanup, which is why
// Reset does not clear it — a Reset from testutil.Setup would silently take
// the override with it and leave those tests waiting out the full default.
var timeoutForTest *time.Duration

// SetTimeoutForTest forces a timeout directly, bypassing the parser. It is
// test-only and the CLI never calls it: the user-facing grammar is whole
// seconds from 1 to 86400, which cannot express the sub-second values tests
// need to make a request time out quickly.
func SetTimeoutForTest(d time.Duration) { timeoutForTest = &d }

// ClearTimeoutForTest restores normal resolution. Test-only, as above.
func ClearTimeoutForTest() { timeoutForTest = nil }

// SetTimeout stores a resolved timeout on this App. root's PersistentPreRunE
// and seek's own in-RunE resolution, which DisableFlagParsing forces, are the
// only production callers; both have already applied the grammar, so there is
// no parsing left in the stored value.
func (a *App) SetTimeout(d time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.timeout, a.timeoutSet = d, true
}

// Timeout returns this invocation's timeout, resolving it at most once. The
// config file is consulted only if it must be — only when neither the flag
// nor the environment supplied a value, and only at the first client
// construction, through the same memoised load every other config reader
// shares. Help, discovery and argument errors never get this far, which is
// what keeps them off the file (contract 2.7).
//
// The config candidate is the one source that can fail here rather than in
// PersistentPreRunE, so the rejection is emitted directly: BAD_REQUEST at
// exit 1, the same code and exit every other rejected timeout gets.
func (a *App) Timeout() time.Duration {
	if timeoutForTest != nil {
		return *timeoutForTest
	}
	a.mu.Lock()
	if a.timeoutSet {
		defer a.mu.Unlock()
		return a.timeout
	}
	a.mu.Unlock()

	d, err := xduration.Resolve(DefaultTimeout, a.configTimeoutCandidate())
	if err != nil {
		output.FailErr(output.Err(output.CodeBadRequest, err.Error()))
		return DefaultTimeout // reached only when output.Exit is a test seam
	}
	a.SetTimeout(d)
	return d
}

// ResolveTimeoutEager resolves the two sources that cost nothing to read: the
// flag and the environment. ok is false when neither is present, in which
// case the config file decides and is not read here — root calls this from
// PersistentPreRunE, which runs before every command including the ones that
// must never touch the file.
func ResolveTimeoutEager(flagSet bool, flagValue string) (time.Duration, bool, error) {
	if flagSet {
		d, err := xduration.Parse(flagValue, "--timeout")
		return d, err == nil, err
	}
	raw := os.Getenv("PLEXCTL_TIMEOUT")
	if raw == "" {
		return 0, false, nil
	}
	d, err := xduration.Parse(raw, "$PLEXCTL_TIMEOUT")
	return d, err == nil, err
}

// ResolveTimeout is the whole ladder, read fresh, against the current App's
// config. It is what seek's hand-parsed --timeout goes through, and what the
// grammar's tests drive directly; the memoised production path is Timeout.
//
// The flag does not go through xduration.Resolve. Resolve skips a candidate
// whose value is empty, which is right for an environment variable and a
// config key — absent means unset — and wrong for a flag: `--timeout ""` is
// an explicitly supplied empty value, which is a mistake, not an unset
// source (contract 2.1). So a flag that was Changed is parsed directly and
// its result is the answer whatever it is.
//
// The config candidate is built only when the environment variable is unset,
// so Resolve's eager argument evaluation cannot pull the config file in when
// $PLEXCTL_TIMEOUT already won.
func ResolveTimeout(flagSet bool, flagValue string) (time.Duration, error) {
	if flagSet {
		return xduration.Parse(flagValue, "--timeout")
	}
	candidates := []xduration.Candidate{{Source: "$PLEXCTL_TIMEOUT", Value: os.Getenv("PLEXCTL_TIMEOUT")}}
	if candidates[0].Value == "" {
		candidates = append(candidates, Current().configTimeoutCandidate())
	}
	return xduration.Resolve(DefaultTimeout, candidates...)
}

func (a *App) configTimeoutCandidate() xduration.Candidate {
	return xduration.Candidate{
		Source: "config timeout (" + config.Path() + ")",
		Value:  a.configTimeoutRaw(),
	}
}

// configTimeoutRaw renders the config file's `timeout` value back to text for
// the parser. A TOML integer becomes its decimal digits and is accepted; a
// float, string, bool, array or table renders to something the integer
// grammar is guaranteed to reject, so one code path produces every rejection
// message and every rejection names the value the user actually wrote.
//
// An absent key renders empty, which xduration.Resolve reads as unset.
//
// The read goes through the App's tolerant load, shared with every other
// config reader, so a command that resolves a timeout and then makes three
// requests still touches the file once, and a file that cannot be parsed is
// simply no candidate rather than a PLEX_AUTH_REQUIRED abort that would kill
// auth login before its quarantine-and-merge repair ran.
func (a *App) configTimeoutRaw() string {
	cfg, err := a.TryConfig()
	if err != nil {
		return ""
	}
	raw, ok := cfg["timeout"]
	if !ok {
		return ""
	}
	switch t := raw.(type) {
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		// A TOML float is a distinct type and is never coerced, so 10.0 must
		// be rejected exactly as 10.5 is. Keep the point visible.
		text := strconv.FormatFloat(t, 'g', -1, 64)
		if !strings.ContainsAny(text, ".eE") {
			text += ".0"
		}
		return text
	case string:
		// Quoted, so `timeout = "10"` is rejected and the message shows the
		// quotes that are the actual mistake. An empty string quotes to `""`,
		// which is non-empty and so is rejected rather than read as unset.
		return strconv.Quote(t)
	default:
		return fmt.Sprintf("%v", raw)
	}
}
