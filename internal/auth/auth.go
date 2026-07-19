// Package auth ports plexctl/auth.py: interactive plex.tv sign-in, PMS
// reachability check, config write.
package auth

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/config"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/output"
)

const plexTVSignIn = "https://plex.tv/users/sign_in.json"

// mergeConfigPairs overlays the four auth-managed keys onto whatever's
// already in existing (a corrupt or missing config's TryLoad result — see
// its own doc comment on why login must tolerate rather than abort on
// that). Every other key existing already had — the README-documented
// `timeout` included — survives untouched. TOML round-trips every value as
// a quoted string (config.Save always double-quotes), so a numeric
// `timeout = 10` survives as `timeout = "10"`; DefaultTimeout already
// parses strings, so this is tolerated rather than fixed here — preserving
// TOML types would mean widening KV beyond string, out of scope for this.
func mergeConfigPairs(existing jsonx.J, serverURL, token, defaultClient, clientID string) []config.KV {
	managed := map[string]bool{"server_url": true, "token": true, "default_client": true, "client_id": true}
	extraKeys := make([]string, 0, len(existing))
	for k := range existing {
		if !managed[k] {
			extraKeys = append(extraKeys, k)
		}
	}
	sort.Strings(extraKeys) // existing is a map: iteration order isn't stable without this
	pairs := make([]config.KV, 0, len(extraKeys)+4)
	for _, k := range extraKeys {
		pairs = append(pairs, config.KV{K: k, V: jsonx.AsStr(existing[k])})
	}
	return append(pairs,
		config.KV{K: "server_url", V: serverURL},
		config.KV{K: "token", V: token},
		config.KV{K: "default_client", V: defaultClient},
		config.KV{K: "client_id", V: clientID},
	)
}

// randomClientIDSuffix mirrors uuid.uuid4().hex[:8]: 8 lowercase hex chars.
func randomClientIDSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// validatePMSURL rejects any scheme other than http/https, userinfo,
// fragments, and query strings before the URL is ever used on the network
// — see README Security section. A plain-http scheme is still accepted;
// the caller decides whether to warn.
func validatePMSURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil ||
		parsed.Fragment != "" || parsed.RawFragment != "" || parsed.RawQuery != "" {
		return nil, fmt.Errorf("invalid PMS URL: %s", raw)
	}
	return parsed, nil
}

// readPassword mirrors getpass.getpass: hidden input on a terminal, plain
// line-read fallback (with getpass's stderr warning) when stdin is not a
// tty — a scripted `printf "user\npass\n..." | plexctl auth login` must
// consume the password line instead of silently skipping it.
func readPassword(reader *bufio.Reader) string {
	fmt.Print("  Password: ")
	if term.IsTerminal(int(os.Stdin.Fd())) {
		passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err == nil {
			return strings.TrimSpace(string(passwordBytes))
		}
	}
	fmt.Fprintln(os.Stderr, "Warning: Password input may be echoed.")
	line, _ := reader.ReadString('\n')
	fmt.Println()
	return strings.TrimSpace(line)
}

// Login mirrors auth.login (interactive; prints JSON result or error+exit).
func Login() {
	fmt.Println("Plex.tv credentials (never stored — only the token is saved)")

	reader := bufio.NewReader(os.Stdin)

	fmt.Print("  Username or email: ")
	username, _ := reader.ReadString('\n')
	username = strings.TrimSpace(username)

	password := readPassword(reader)

	fmt.Printf("  PMS URL [%s]: ", config.Defaults["server_url"])
	serverURL, _ := reader.ReadString('\n')
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		serverURL = config.Defaults["server_url"]
	}

	parsedURL, err := validatePMSURL(serverURL)
	if err != nil {
		output.FailErr(output.Err(output.CodeBadRequest, err.Error()))
		return
	}
	if parsedURL.Scheme == "http" {
		fmt.Fprintln(os.Stderr, "Warning: plain-HTTP PMS URL — the token will be sent unencrypted. Use only on a trusted local network.")
	}

	fmt.Printf("  Default client [%s]: ", config.Defaults["default_client"])
	defaultClient, _ := reader.ReadString('\n')
	defaultClient = strings.TrimSpace(defaultClient)
	if defaultClient == "" {
		defaultClient = config.Defaults["default_client"]
	}

	var clientID string
	if v, ok := config.Load()["client_id"]; ok && jsonx.Truthy(v) {
		clientID = jsonx.AsStr(v)
	} else {
		clientID = "plexctl-" + randomClientIDSuffix()
	}

	// Headers for plex.tv sign-in — deliberately not api.Headers: no
	// X-Plex-Provides, no token (there isn't one yet).
	headers := map[string]string{
		"X-Plex-Product":           "plexctl",
		"X-Plex-Version":           api.Version,
		"X-Plex-Platform":          "Go",
		"Accept":                   "application/json",
		"X-Plex-Client-Identifier": clientID,
	}

	// Request-build failure (effectively unreachable) classifies like the
	// PMS-verify build site: cloud target, transport class.
	req, err := http.NewRequest(http.MethodPost, plexTVSignIn, nil)
	if err != nil {
		e := api.AsError(err)
		e.Message = "auth request failed: " + e.Message
		output.FailErr(api.Classify(e, api.TargetCloud))
		return
	}
	req.SetBasicAuth(username, password)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// Dial timeout slightly under the overall deadline so a connect stall
	// reliably classifies as a dial error ("connection failed", matching
	// requests.ConnectTimeout ⊂ ConnectionError) rather than racing the
	// phase-blind Client.Timeout.
	client := api.NewHTTPClient(15*time.Second, &http.Transport{
		DialContext: (&net.Dialer{Timeout: 14 * time.Second}).DialContext,
	})
	resp, err := client.Do(req)
	if err != nil {
		output.FailErr(api.Classify(api.AsError(err), api.TargetCloud))
		return
	}
	defer resp.Body.Close()
	// plex.tv sign-in responses are small; the 32 MiB cap just matches the
	// PMS/Companion bounded reads, and truncation would surface as a JSON
	// parse error downstream, not a sentinel to handle.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		output.FailErr(api.Classify(api.AsError(err), api.TargetCloud))
		return
	}
	const authFailedHint = "check credentials and retry: plexctl auth login"
	if resp.StatusCode >= 400 {
		output.FailErr(output.Err(output.CodeAuthFailed, fmt.Sprintf("auth failed: HTTP %d", resp.StatusCode)).WithHint(authFailedHint))
		return
	}

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		output.FailErr(output.Err(output.CodeAuthFailed, fmt.Sprintf("plex.tv returned non-JSON response: %s", err.Error())).WithHint(authFailedHint))
		return
	}
	payloadMap, ok := payload.(map[string]any)
	if !ok {
		output.FailErr(output.Err(output.CodeAuthFailed, "unexpected auth response shape from plex.tv").WithHint(authFailedHint))
		return
	}
	user, ok := payloadMap["user"].(map[string]any)
	if !ok {
		output.FailErr(output.Err(output.CodeAuthFailed, "unexpected auth response shape from plex.tv").WithHint(authFailedHint))
		return
	}
	token, ok := user["authToken"].(string)
	if !ok {
		output.FailErr(output.Err(output.CodeAuthFailed, "unexpected auth response shape from plex.tv").WithHint(authFailedHint))
		return
	}

	// Verify PMS is reachable before writing config
	verifyReq, err := http.NewRequest(http.MethodGet, strings.TrimRight(serverURL, "/")+"/", nil)
	if err != nil {
		output.FailErr(api.Classify(api.AsError(err), api.TargetPMS))
		return
	}
	for k, v := range headers {
		verifyReq.Header.Set(k, v)
	}
	verifyReq.Header.Set("X-Plex-Token", token)
	verifyClient := api.NewHTTPClient(10*time.Second, nil)
	verifyResp, err := verifyClient.Do(verifyReq)
	if err != nil {
		output.FailErr(api.Classify(api.AsError(err), api.TargetPMS))
		return
	}
	defer verifyResp.Body.Close()
	if verifyResp.StatusCode >= 400 {
		// Wrong URL/token, not a transport problem — keep the "PMS
		// unreachable at …" message text (pre-v2 wording), same hint as the
		// other PLEX_AUTH_FAILED sites above.
		output.FailErr(output.Err(output.CodeAuthFailed,
			fmt.Sprintf("PMS unreachable at %s: %d %s", serverURL, verifyResp.StatusCode, http.StatusText(verifyResp.StatusCode))).
			WithHint("check credentials and retry: plexctl auth login"))
		return
	}

	// W5: this used to Save only the four keys below, which is a
	// specification bug inherited from Python (auth.py:72 passes the same
	// four keys to config.py's write_text-of-only-that-dict) rather than a
	// port regression — but it silently destroyed any other hand-added key
	// (the README-documented `timeout` included). mergeConfigPairs merges
	// onto whatever's already there instead of overwriting it.
	existing, _ := config.TryLoad()
	pairs := mergeConfigPairs(existing, serverURL, token, defaultClient, clientID)

	// Python's cfg.save() propagates filesystem errors (traceback, exit 1);
	// the Go equivalent is the standard JSON error + exit 1 — never a false
	// "token saved" success.
	if err := config.Save(pairs); err != nil {
		output.FailErr(output.Err(output.CodeInternal, fmt.Sprintf("failed to write config at %s: %s", config.Path(), err.Error())))
		return
	}

	output.Print(jsonx.J{"ok": true, "message": fmt.Sprintf("token saved to %s", config.Path())})
}
