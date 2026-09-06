// Package app holds the per-invocation values plexctl resolves once and
// reads from many places: the config file, and (from item 13) the timeout.
//
// The value is process-scoped by design, not by accident. Contract 2.7 asks
// for "a lazily loaded per-invocation config on App" and does not say how the
// read sites reach it. Threading an *App through api.Get, api.TryGet and
// their siblings would change six exported signatures and about fifty domain
// call sites for no observable behaviour, against 11k lines of tests, so root
// calls Set once and the read sites call Current(). Phase C3b is where an
// injected writer and real dependency injection land; this package is the
// seam that pass will replace. See docs/DECISIONS.md.
package app

import (
	"sync"

	"github.com/corinthian/plexctl/internal/config"
	"github.com/corinthian/plexctl/internal/jsonx"
)

// App is one invocation's resolved state. Every field is lazy: constructing
// an App reads nothing, which is what keeps --help, `commands` and an
// argument error off the config file entirely.
type App struct {
	mu      sync.Mutex
	loaded  bool
	cfg     jsonx.J
	loadErr error
}

// New returns an App that has read nothing yet.
func New() *App { return &App{} }

var (
	currentMu sync.Mutex
	current   = New()
)

// Set installs the App for this invocation. root's PersistentPreRunE is the
// only production caller.
func Set(a *App) {
	if a == nil {
		a = New()
	}
	currentMu.Lock()
	current = a
	currentMu.Unlock()
}

// Current returns the invocation's App. It is never nil: a direct package
// call in a test, with no root ever built, gets a fresh App that loads on
// demand exactly as a real invocation would.
func Current() *App {
	currentMu.Lock()
	defer currentMu.Unlock()
	return current
}

// Reset installs a fresh App, discarding anything memoised. Test seam:
// testutil.Setup calls it, so a test that redirects PLEXCTL_CONFIG_DIR never
// inherits the previous test's config.
func Reset() { Set(New()) }

// load performs the one config read, tolerantly. TryLoad, not Load: the
// timeout candidate reads the same result, and aborting there at
// PLEX_AUTH_REQUIRED would kill auth login, the one command whose job is to
// quarantine and repair an unusable config. Callers that need a usable config
// turn the stored error into the print-and-exit envelope themselves.
func (a *App) load() (jsonx.J, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.loaded {
		a.cfg, a.loadErr = config.TryLoad()
		a.loaded = true
	}
	return a.cfg, a.loadErr
}

// TryConfig returns the invocation's config and the load error, without the
// print-and-exit failure. The timeout's config candidate reads through this:
// a file that cannot be parsed has no readable timeout in it either, so a
// load failure there is simply no candidate, and auth login — the one command
// whose job is to quarantine and repair an unusable config — is not aborted
// before it runs (contract Part 3, plexctl row "Config unparseable, auth
// login", marked unchanged). Every genuine config-failure row still fires
// where the config is actually needed, at Config and at Require.
func (a *App) TryConfig() (jsonx.J, error) { return a.load() }

// Config returns the invocation's config, loading it at most once. A load
// failure fails through config.FailUnusable — the same PLEX_AUTH_REQUIRED
// envelope at exit 5 that config.Load has always produced, on every call, so
// memoising the read does not memoise away the failure.
func (a *App) Config() jsonx.J {
	cfg, err := a.load()
	if err != nil {
		return config.FailUnusable(err)
	}
	return cfg
}

// Require is config.Require against the memoised config: the load failure
// fires first, then the missing-key failure, in that order and with the same
// envelopes.
func (a *App) Require(key string) string {
	return config.RequireFrom(a.Config(), key)
}
