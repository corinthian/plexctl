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
)

// Version is the X-Plex-Version header value and the CLI version.
const Version = "0.9.0"

const plexTV = "https://plex.tv"

// DefaultTimeoutSeconds matches api.DEFAULT_TIMEOUT.
const DefaultTimeoutSeconds = 10.0

var timeoutOverride *float64

// SetTimeoutOverride sets the process-wide timeout (the CLI's --timeout flag
// lands here).
func SetTimeoutOverride(seconds float64) {
	timeoutOverride = &seconds
}

// ClearTimeoutOverride mirrors set_timeout_override(None) — tests need it;
// the CLI never does.
func ClearTimeoutOverride() {
	timeoutOverride = nil
}

// DefaultTimeout resolves the per-request timeout:
// --timeout > $PLEXCTL_TIMEOUT > config `timeout` > 10s.
func DefaultTimeout() float64 {
	if timeoutOverride != nil {
		return *timeoutOverride
	}
	if raw := os.Getenv("PLEXCTL_TIMEOUT"); raw != "" {
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			return f
		}
	}
	if raw, ok := config.Load()["timeout"]; ok && jsonx.Truthy(raw) {
		switch t := raw.(type) {
		case float64:
			return t
		case int64:
			return float64(t)
		case string:
			if f, err := strconv.ParseFloat(t, 64); err == nil {
				return f
			}
		}
	}
	return DefaultTimeoutSeconds
}

// Error mirrors PlexAPIError. Message is JSON-safe; Kind is "timeout" for
// connect/read timeouts, "error" otherwise — batch callers retry timeouts
// but not hard failures, and the CLI maps the distinction to exit codes
// (2 vs 1).
type Error struct {
	Message string
	Kind    string
}

func (e *Error) Error() string { return e.Message }

// Headers returns the standard Plex header set. X-Plex-Provides: controller
// is required on every PMS request or /clients returns an empty list.
func Headers(token, clientID string) map[string]string {
	return map[string]string{
		"X-Plex-Product":            "plexctl",
		"X-Plex-Version":            Version,
		"X-Plex-Platform":           "Go",
		"X-Plex-Provides":           "controller",
		"Accept":                    "application/json",
		"X-Plex-Token":              token,
		"X-Plex-Client-Identifier":  clientID,
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

func classifyTransport(err error) *Error {
	var ne net.Error
	if (errors.As(err, &ne) && ne.Timeout()) || errors.Is(err, context.DeadlineExceeded) {
		// Before the connection-failed branch: a connect timeout must
		// classify as a timeout (kind/exit-code contract), mirroring the
		// ConnectTimeout-subclasses-both ordering note in api.py.
		return &Error{Message: "request timed out: " + err.Error(), Kind: "timeout"}
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return &Error{Message: "connection failed: " + err.Error(), Kind: "error"}
	}
	return &Error{Message: "request failed: " + err.Error(), Kind: "error"}
}

// Request performs an HTTP call against base+path, mirroring api._request.
// timeout <= 0 means "use the resolved default". The return is any because
// plex.tv endpoints return JSON arrays; PMS endpoints return objects.
func Request(method, base, path string, params url.Values, timeout float64) (any, error) {
	cfg := config.Load()
	token := config.Require("token")
	clientID := config.StringOr(cfg, "client_id", config.Defaults["client_id"])
	if timeout <= 0 {
		timeout = DefaultTimeout()
	}
	req, err := http.NewRequest(method, BuildURL(base, path, params), nil)
	if err != nil {
		return nil, &Error{Message: "request failed: " + err.Error(), Kind: "error"}
	}
	for k, v := range Headers(token, clientID) {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: time.Duration(timeout * float64(time.Second))}
	resp, err := client.Do(req)
	if err != nil {
		return nil, classifyTransport(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, classifyTransport(err)
	}
	if resp.StatusCode >= 400 {
		reason := http.StatusText(resp.StatusCode)
		return nil, &Error{Message: FormatHTTPError(resp.StatusCode, resp.Header.Get("Content-Type"), string(body), reason), Kind: "error"}
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

// ExitOnError wraps Request with print-and-exit semantics (api._exit_on_error).
func ExitOnError(method, base, path string, params url.Values, prefix string) any {
	v, err := Request(method, base, path, params, 0)
	if err != nil {
		var e *Error
		if !errors.As(err, &e) {
			e = &Error{Message: err.Error(), Kind: "error"}
		}
		output.Print(jsonx.J{"ok": false, "error": prefix + e.Message})
		if e.Kind == "timeout" {
			output.Exit(2)
		} else {
			output.Exit(1)
		}
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
	v, err := Request("GET", ServerBase(), path, params, 0)
	if err != nil {
		return nil, err
	}
	return asJ(v), nil
}

func TryPut(path string, params url.Values) (jsonx.J, error) {
	v, err := Request("PUT", ServerBase(), path, params, 0)
	if err != nil {
		return nil, err
	}
	return asJ(v), nil
}

func TryDelete(path string, params url.Values) (jsonx.J, error) {
	v, err := Request("DELETE", ServerBase(), path, params, 0)
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
	return fmt.Sprintf("HTTP %d: %s", status, detail)
}
