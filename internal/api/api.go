// Package api ports plexctl/api.py: HTTP wrappers for PMS and plex.tv with
// the five-way error ladder, timeout resolution, and print-and-exit or
// try-and-recover calling conventions.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/corinthian/plexctl/internal/config"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/output"
	"github.com/corinthian/plexctl/internal/xduration"
)

// Version is the X-Plex-Version header value and the CLI version. It's a
// var, not a const, so build.sh can inject the real value via
// -ldflags -X — a const can't be overridden that way. This default is
// what an unadorned `go build` (dev builds, tests) reports.
var Version = "2.0.0-dev"

const plexTV = "https://plex.tv"

// DefaultTimeout is the timeout used when no source supplies one.
const DefaultTimeout = 10 * time.Second

// resolvedTimeout is the per-invocation timeout, resolved once by root's
// PersistentPreRunE and read by every client constructor. It is a resolved
// value with no parsing left in it: the grammar is enforced at the boundary,
// in ResolveTimeout.
var resolvedTimeout = DefaultTimeout

// timeoutForTest, when set, wins over the resolved value. It has to: root's
// PersistentPreRunE resolves on every invocation, so a test that forces a
// sub-second timeout and then runs a command through the root would have it
// overwritten before the first request.
var timeoutForTest *time.Duration

// SetTimeout stores the resolved timeout. root's PersistentPreRunE (and
// seek's own in-RunE resolution, which DisableFlagParsing forces) are the
// only production callers.
func SetTimeout(d time.Duration) { resolvedTimeout = d }

// Timeout returns the resolved per-invocation timeout.
func Timeout() time.Duration {
	if timeoutForTest != nil {
		return *timeoutForTest
	}
	return resolvedTimeout
}

// SetTimeoutForTest forces a timeout directly, bypassing the parser. It is
// test-only and the CLI never calls it: the user-facing grammar is whole
// seconds from 1 to 86400, which cannot express the sub-second values tests
// need to make a request time out quickly.
func SetTimeoutForTest(d time.Duration) { timeoutForTest = &d }

// ClearTimeoutForTest restores the default. Test-only, as above.
func ClearTimeoutForTest() { timeoutForTest = nil }

// ResolveTimeout resolves the timeout from --timeout, $PLEXCTL_TIMEOUT and
// the config file, in that order, under xduration's integer-seconds grammar.
//
// The flag does not go through xduration.Resolve. Resolve skips a candidate
// whose value is empty, which is right for an environment variable and a
// config key — absent means unset — and wrong for a flag: `--timeout ""` is
// an explicitly supplied empty value, which is a mistake, not an unset
// source (contract 2.1). So a flag that was Changed is parsed directly and
// its result is the answer whatever it is.
//
// The config candidate is built only when the environment variable is unset.
// Resolve would otherwise have its argument evaluated eagerly, reading the
// config file even when $PLEXCTL_TIMEOUT wins — widening the load frequency
// that contract 2.7 is narrowing.
func ResolveTimeout(flagSet bool, flagValue string) (time.Duration, error) {
	if flagSet {
		return xduration.Parse(flagValue, "--timeout")
	}
	candidates := []xduration.Candidate{{Source: "$PLEXCTL_TIMEOUT", Value: os.Getenv("PLEXCTL_TIMEOUT")}}
	if candidates[0].Value == "" {
		candidates = append(candidates, xduration.Candidate{
			Source: "config timeout (" + config.Path() + ")",
			Value:  configTimeoutRaw(),
		})
	}
	return xduration.Resolve(DefaultTimeout, candidates...)
}

// configTimeoutRaw renders the config file's `timeout` value back to text for
// the parser. A TOML integer becomes its decimal digits and is accepted; a
// float, string, bool, array or table renders to something the integer
// grammar is guaranteed to reject, so one code path produces every rejection
// message and every rejection names the value the user actually wrote.
//
// An absent key renders empty, which xduration.Resolve reads as unset.
func configTimeoutRaw() string {
	raw, ok := config.Load()["timeout"]
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

// Error mirrors PlexAPIError. Message is JSON-safe; Kind is "timeout" for
// connect/read timeouts, "error" otherwise — batch callers retry timeouts
// but not hard failures, and the CLI maps the distinction to exit codes
// (2 vs 1). Status carries the HTTP status for >=400 responses (0 for
// transport/parse errors) so callers can distinguish a pruned queue (404)
// from other failures.
type Error struct {
	Message string
	Kind    string
	Status  int
}

func (e *Error) Error() string { return e.Message }

// Headers returns the standard Plex header set. X-Plex-Provides: controller
// is required on every PMS request or /clients returns an empty list.
func Headers(token, clientID string) map[string]string {
	return map[string]string{
		"X-Plex-Product":           "plexctl",
		"X-Plex-Version":           Version,
		"X-Plex-Platform":          "Go",
		"X-Plex-Provides":          "controller",
		"Accept":                   "application/json",
		"X-Plex-Token":             token,
		"X-Plex-Client-Identifier": clientID,
	}
}

// ServerBase is the configured PMS base URL.
func ServerBase() string {
	return config.StringOr(config.Load(), "server_url", config.Defaults["server_url"])
}

// BuildURL joins base+path and appends params. path may already carry a
// query string (smart-collection content paths do).
func BuildURL(base, path string, params url.Values) string {
	u := strings.TrimRight(base, "/") + path
	if len(params) > 0 {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + params.Encode()
	}
	return u
}

// NewHTTPClient returns the shared client: bounded timeout, no redirects.
// Nothing plexctl calls legitimately redirects; following one can forward
// X-Plex-Token to an arbitrary destination (Go only strips Authorization/
// Cookie-class headers cross-origin). CheckRedirect fires BEFORE the
// redirect request is sent, so refusing here means no header ever leaves.
func NewHTTPClient(timeout time.Duration, transport http.RoundTripper) *http.Client {
	c := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("redirect refused: destination %s://%s%s", req.URL.Scheme, req.URL.Host, req.URL.Path)
		},
	}
	if transport != nil {
		c.Transport = transport
	}
	return c
}

// SanitizeError renders err without query strings, userinfo, or fragments.
// url.Error's rendered form embeds the full request URL; private data rides
// its query. Keep op, scheme, host, port, and path — the skill routes
// errors by URL shape (32500 vs 32400 vs plex.tv).
func SanitizeError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		if parsed, perr := url.Parse(ue.URL); perr == nil {
			return ue.Op + " \"" + parsed.Scheme + "://" + parsed.Host + parsed.Path + "\": " + SanitizeError(ue.Err)
		}
		return ue.Op + ": " + SanitizeError(ue.Err)
	}
	return err.Error()
}

func classifyTransport(err error) *Error {
	var ne net.Error
	if (errors.As(err, &ne) && ne.Timeout()) || errors.Is(err, context.DeadlineExceeded) {
		// Before the connection-failed branch: a connect timeout must
		// classify as a timeout (kind/exit-code contract), mirroring the
		// ConnectTimeout-subclasses-both ordering note in api.py.
		return &Error{Message: "request timed out: " + SanitizeError(err), Kind: "timeout"}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return &Error{Message: "connection failed: " + SanitizeError(err), Kind: "error"}
	}
	return &Error{Message: "request failed: " + SanitizeError(err), Kind: "error"}
}

// Request performs an HTTP call against base+path, mirroring api._request.
// The return is any because plex.tv endpoints return JSON arrays; PMS
// endpoints return objects.
func Request(method, base, path string, params url.Values) (any, error) {
	cfg := config.Load()
	token := config.Require("token")
	clientID := config.StringOr(cfg, "client_id", config.Defaults["client_id"])
	req, err := http.NewRequest(method, BuildURL(base, path, params), nil)
	if err != nil {
		return nil, &Error{Message: "request failed: " + err.Error(), Kind: "error"}
	}
	for k, v := range Headers(token, clientID) {
		req.Header.Set(k, v)
	}
	client := NewHTTPClient(Timeout(), nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, classifyTransport(err)
	}
	defer resp.Body.Close()
	// PMS library responses are legitimately large; 32 MiB just yields a
	// JSON parse error downstream on truncation, not a sentinel to handle.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, classifyTransport(err)
	}
	if resp.StatusCode >= 400 {
		reason := http.StatusText(resp.StatusCode)
		return nil, &Error{Message: FormatHTTPError(resp.StatusCode, resp.Header.Get("Content-Type"), string(body), reason), Kind: "error", Status: resp.StatusCode}
	}
	if strings.TrimSpace(string(body)) == "" {
		return jsonx.J{}, nil
	}
	// UseNumber keeps PMS number literals verbatim through the pass-through
	// paths (9.0 stays 9.0, like Python's json round-trip), instead of
	// float64's shortest-form re-rendering.
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, &Error{Message: "invalid JSON response: " + err.Error(), Kind: "error"}
	}
	return v, nil
}

// Target names which leg of the stack a request went to. Callers declare it
// statically — the v1 skill had to sniff URLs (32500 vs 32400 vs plex.tv) out
// of free-text errors to route them; in v2 that classification is done here,
// at the call that already knows.
type Target int

const (
	TargetPMS Target = iota
	TargetCloud
	TargetClient
)

// Classify converts an api.Error into the v2 CLIError per
// docs/error_model_v2.md §3: transport errors code by target, HTTP statuses
// by class. The chokepoint for every coded failure that starts as HTTP.
func Classify(e *Error, target Target) *output.CLIError {
	if e.Status == 0 {
		switch target {
		case TargetCloud:
			return output.Err(output.CodeCloudUnreachable, e.Message).
				WithHint("plex.tv is unreachable — the local server is unaffected; retry shortly")
		case TargetClient:
			return output.Err(output.CodeClientUnreachable, e.Message).
				WithHint("wake the device or relaunch Plex on it, then retry")
		default:
			if e.Kind == "timeout" {
				return output.Err(output.CodeTransportTimeout, e.Message).
					WithHint("retry — on batches, retry only timed-out items")
			}
			return output.Err(output.CodeTransportFailed, e.Message)
		}
	}
	switch {
	case e.Status == 401 || e.Status == 403:
		return output.Err(output.CodeAuthRequired, e.Message).
			WithHTTPStatus(e.Status).WithHint("run: plexctl auth login")
	case e.Status == 404:
		return output.Err(output.CodeNotFound, e.Message).WithHTTPStatus(e.Status)
	case e.Status == 400:
		return output.Err(output.CodeBadRequest, e.Message).WithHTTPStatus(e.Status)
	case e.Status >= 500:
		return output.Err(output.CodeServerError, e.Message).WithHTTPStatus(e.Status)
	default:
		return output.Err(output.CodeHTTPError, e.Message).WithHTTPStatus(e.Status)
	}
}

// AsError normalizes any error to *Error (non-api errors become Kind "error").
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Message: err.Error(), Kind: "error"}
}

// ExitOnError wraps Request with print-and-exit semantics (api._exit_on_error),
// emitting the v2 coded envelope. prefix survives in the human-readable
// message only; routing rides the code.
func ExitOnError(method, base, path string, params url.Values, prefix string) any {
	v, err := Request(method, base, path, params)
	if err != nil {
		target := TargetPMS
		if base == plexTV {
			target = TargetCloud
		}
		e := AsError(err)
		cli := Classify(e, target)
		cli.Message = prefix + e.Message
		output.FailErr(cli)
		return jsonx.J{} // reached only when output.Exit is a test seam
	}
	return v
}

func asJ(v any) jsonx.J {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return jsonx.J{}
}

// Get / Post / Put / Delete hit PMS with print-and-exit semantics.
func Get(path string, params url.Values) jsonx.J {
	return asJ(ExitOnError("GET", ServerBase(), path, params, ""))
}

func Post(path string, params url.Values) jsonx.J {
	return asJ(ExitOnError("POST", ServerBase(), path, params, ""))
}

func Put(path string, params url.Values) jsonx.J {
	return asJ(ExitOnError("PUT", ServerBase(), path, params, ""))
}

func Delete(path string, params url.Values) jsonx.J {
	return asJ(ExitOnError("DELETE", ServerBase(), path, params, ""))
}

// PlexTVGet hits plex.tv; the response may be a JSON array (devices.json).
func PlexTVGet(path string, params url.Values) any {
	return ExitOnError("GET", plexTV, path, params, "plex.tv ")
}

// TryGet / TryPut / TryDelete raise instead of print-and-exit, for callers
// that recover (fallbacks, best-effort deletes, per-item batch tolerance).
func TryGet(path string, params url.Values) (jsonx.J, error) {
	v, err := Request("GET", ServerBase(), path, params)
	if err != nil {
		return nil, err
	}
	return asJ(v), nil
}

func TryPut(path string, params url.Values) (jsonx.J, error) {
	v, err := Request("PUT", ServerBase(), path, params)
	if err != nil {
		return nil, err
	}
	return asJ(v), nil
}

func TryDelete(path string, params url.Values) (jsonx.J, error) {
	v, err := Request("DELETE", ServerBase(), path, params)
	if err != nil {
		return nil, err
	}
	return asJ(v), nil
}

// --- format_http_error port --------------------------------------------------

var (
	titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	tagRe   = regexp.MustCompile(`<[^>]+>`)
	wsRe    = regexp.MustCompile(`\s+`)
)

func stripHTML(body string) string {
	return strings.TrimSpace(wsRe.ReplaceAllString(tagRe.ReplaceAllString(body, " "), " "))
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func jsonErrorDetail(body string) string {
	var data any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return ""
	}
	m, ok := data.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"error", "message", "Error", "Message", "errorMessage"} {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if errs, ok := m["errors"].([]any); ok && len(errs) > 0 {
		switch first := errs[0].(type) {
		case string:
			return strings.TrimSpace(first)
		case map[string]any:
			for _, key := range []string{"message", "error", "detail"} {
				if v, ok := first[key].(string); ok && strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
		}
	}
	return ""
}

// FormatHTTPError mirrors api.format_http_error: HTML bodies prefer <title>,
// falling back to stripped tags or the reason phrase; JSON bodies probe the
// common error fields; anything else truncates to 200 chars.
func FormatHTTPError(status int, ctype, body, reason string) string {
	reason = strings.TrimSpace(reason)
	body = strings.TrimSpace(body)
	ctype = strings.ToLower(ctype)

	head := strings.ToLower(truncRunes(body, 100))
	head = strings.TrimLeft(head, " \t\r\n\f\v")
	isHTML := strings.Contains(ctype, "text/html") ||
		strings.HasPrefix(head, "<!doctype") || strings.HasPrefix(head, "<html")
	isJSON := strings.Contains(ctype, "application/json") ||
		strings.HasPrefix(body, "{") || strings.HasPrefix(body, "[")

	detail := ""
	switch {
	case isHTML:
		if m := titleRe.FindStringSubmatch(body); m != nil {
			detail = strings.TrimSpace(wsRe.ReplaceAllString(m[1], " "))
		}
		if detail == "" {
			if stripped := stripHTML(body); stripped != "" {
				detail = truncRunes(stripped, 200)
			} else {
				detail = reason
			}
		}
	case isJSON:
		detail = jsonErrorDetail(body)
		if detail == "" {
			detail = truncRunes(body, 200)
		}
	default:
		if body != "" {
			detail = truncRunes(body, 200)
		} else {
			detail = reason
		}
	}

	if detail == "" {
		detail = reason
		if detail == "" {
			detail = "no response body"
		}
	}
	return fmt.Sprintf("HTTP %d: %s", status, stripControlChars(detail))
}

// stripControlChars removes ASCII control characters from a remote-supplied
// string before it reaches a terminal or log — \n and \t become a space
// (preserving word boundaries) rather than vanishing; everything else below
// 0x20, plus DEL (0x7F), is dropped outright.
func stripControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7F:
			// drop
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
