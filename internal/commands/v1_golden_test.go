package commands_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/testutil"
)

// --- v1 golden fixtures (P0.3) ------------------------------------------------
//
// These pin the CURRENT (v1) stdout/exit-code contract for a representative
// set of commands, ahead of the v2 error-model rebuild that will deliberately
// change some of these shapes (exit codes, error envelopes, staged/
// clientUnreachable semantics). Fixtures live in test/goldens/v1_*.golden —
// see test/goldens/README.md for the format and how each was captured.
//
// This is a different fixture role than test/goldens/queue-show.json and
// history-5.json (those are PMS *response* bodies fed into the fake server;
// internal/queue's loadGolden reads them for that purpose). The v1_*.golden
// files here capture the CLI's own {argv, stdout, exit_code} contract.

type v1Golden struct {
	Argv     []string `json:"argv"`
	Stdout   string   `json:"stdout"`
	ExitCode int      `json:"exit_code"`
}

func loadV1Golden(t *testing.T, name string) v1Golden {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "goldens", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	var g v1Golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatalf("parse golden %s: %v", name, err)
	}
	return g
}

// runForGolden executes argv (without the "plexctl" program-name element)
// through the same root-command seam every other commands_test.go test uses,
// mapping testutil.Capture's -1 "no output.Exit call" sentinel to the real
// process exit code of 0 (see execute_test.go's --help/--version case).
func runForGolden(t *testing.T, argv []string) (stdout string, exitCode int) {
	t.Helper()
	root := commands.BuildRoot()
	root.SetArgs(argv[1:])
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code == -1 {
		code = 0
	}
	return out, code
}

func assertGolden(t *testing.T, g v1Golden, gotStdout string, gotCode int) {
	t.Helper()
	if gotStdout != g.Stdout {
		t.Fatalf("stdout drifted from golden\n got:  %q\n want: %q", gotStdout, g.Stdout)
	}
	if gotCode != g.ExitCode {
		t.Fatalf("exit code = %d, want %d (golden argv %v)", gotCode, g.ExitCode, g.Argv)
	}
}

// TestV1GoldenSearchHit pins search's summary shape (ratingKey/title/type/
// year) for a single confident hit — the common case, previously uncovered
// by an exact-stdout snapshot (existing search tests check no-match and
// choice-validation paths, never a hit's literal JSON).
func TestV1GoldenSearchHit(t *testing.T) {
	g := loadV1Golden(t, "v1_search_hit.golden")
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search", map[string]any{
		"MediaContainer": map[string]any{
			"Hub": []any{
				map[string]any{"type": "movie", "Metadata": []any{
					map[string]any{"ratingKey": "100301", "title": "The Birds", "type": "movie", "score": "0.93080", "year": 1963.0},
				}},
			},
		},
	})
	out, code := runForGolden(t, g.Argv)
	assertGolden(t, g, out, code)
}

// TestV1GoldenSearchNoResults pins the exact "no matches" envelope (ok:true,
// empty results, note). TestSearchEmptyResultsOkTrueWithNote (library_test.go)
// already checks the individual fields; this fixture pins the whole line.
func TestV1GoldenSearchNoResults(t *testing.T) {
	g := loadV1Golden(t, "v1_search_no_results.golden")
	f := newFakePMS(t)
	f.onJSON("GET", "/hubs/search/voice", map[string]any{"MediaContainer": map[string]any{}})
	f.onJSON("GET", "/hubs/search", map[string]any{"MediaContainer": map[string]any{}})
	out, code := runForGolden(t, g.Argv)
	assertGolden(t, g, out, code)
}

// TestV1GoldenPlayIdleClient pins `play` against a resolvable client with no
// wired /status/sessions data (idle — nothing playing). playerCmd never
// checks session state, so v1 sends the Companion play command regardless
// and reports ok:true unconditionally. A v2 error model that wants "nothing
// to resume" on an idle client would change this exact line — that's the
// behavior this fixture exists to catch.
func TestV1GoldenPlayIdleClient(t *testing.T) {
	g := loadV1Golden(t, "v1_play_idle_client.golden")
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onStatus("GET", "/player/playback/play", 200)
	out, code := runForGolden(t, g.Argv)
	assertGolden(t, g, out, code)
}

// TestV1GoldenQueueBindHTTP500Staged pins the queue lifecycle's bind-failure
// (staged) path: a reachable client answers the bind with an HTTP error
// (not a transport failure), so the new queue's IDs are still surfaced,
// staged:true records it for `queue-start` recovery, and clientUnreachable
// is absent (that key is reserved for the transport-timeout shape — see
// TestV1GoldenRequestTimeout / TestQueueBindTimeoutStagesQueueWithClientUnreachable
// for that path, which already has thorough inline key-assertion coverage).
func TestV1GoldenQueueBindHTTP500Staged(t *testing.T) {
	g := loadV1Golden(t, "v1_queue_bind_http500_staged.golden")
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/", map[string]any{"MediaContainer": map[string]any{"machineIdentifier": "srv-1"}})
	f.onJSON("POST", "/playQueues", map[string]any{
		"MediaContainer": map[string]any{"playQueueID": "555", "playQueueSelectedItemID": "999"},
	})
	f.onStatus("GET", "/player/playback/playMedia", 500)
	out, code := runForGolden(t, g.Argv)
	assertGolden(t, g, out, code)
}

// TestV1GoldenAuthMissing pins the no-token bootstrap failure end-to-end
// through a real CLI invocation (now-playing) — config_test.go's
// TestRequireMissingExitsOne already covers config.Require("token") in
// isolation; this fixture pins the same message reached via argv, before
// any command logic (and before any network dial) runs.
func TestV1GoldenAuthMissing(t *testing.T) {
	g := loadV1Golden(t, "v1_auth_missing_token.golden")
	f := newFakePMS(t)
	// Overwrite config.toml to drop the token line testutil.Setup wrote,
	// keeping server_url pointed at the fake server so even a regression
	// that reordered the token check ahead of a dial couldn't reach the
	// real network.
	cfg := "server_url = \"" + f.srv.URL + "\"\n" +
		"default_client = \"Apple TV\"\n" +
		"client_id = \"plexctl-test\"\n"
	if err := os.WriteFile(filepath.Join(f.dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := runForGolden(t, g.Argv)
	assertGolden(t, g, out, code)
}

// timeoutReasonRe collapses the one part of Go's client-timeout error text
// that races: depending on exactly when the context deadline fires relative
// to the transport's own cancellation, http.Client.Do returns either a
// "context deadline exceeded" or a "net/http: request canceled" leading
// clause for the identical timeout — both wrapped in the same
// "(Client.Timeout exceeded while awaiting headers)" suffix. Observed both
// variants across otherwise-identical runs of this exact test.
var timeoutReasonRe = regexp.MustCompile(`(?:net/http: request canceled|context deadline exceeded)(?: \(Client\.Timeout exceeded while awaiting headers\))?`)

// TestV2GoldenRequestTimeout pins the request-timeout contract via a plain
// single-request command (now-playing), isolated from queue's staging side
// effects. MIGRATED to v2 when the api.ExitOnError chokepoint recoded:
// TRANSPORT_TIMEOUT, exit 3, structured envelope (the v1 fixture lives in git
// history). Two parts of the captured stdout are nondeterministic across
// runs and must be normalized (in both the captured output and the stored
// golden) before an exact-string comparison: the fake server's host:port (a
// fresh httptest.Server per run) and the timeout error's leading clause (see
// timeoutReasonRe) — an unnormalized exact match would pass once and then
// flake.
func TestV2GoldenRequestTimeout(t *testing.T) {
	g := loadV1Golden(t, "v2_request_timeout.golden")
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.on("GET", "/status/sessions", func(r *http.Request) (int, any) {
		time.Sleep(250 * time.Millisecond)
		return 200, nil
	})
	api.SetTimeoutOverride(0.05)
	t.Cleanup(api.ClearTimeoutOverride)

	out, code := runForGolden(t, g.Argv)

	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ReplaceAll(out, u.Host, "<SERVER>")
	normalized = timeoutReasonRe.ReplaceAllString(normalized, "<TIMEOUT-REASON>")
	assertGolden(t, g, normalized, code)
}
