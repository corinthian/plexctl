// Package auth ports plexctl/auth.py: interactive plex.tv sign-in, PMS
// reachability check, config write.
package auth

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	"github.com/corinthian/plexctl/internal/xhttp"
)

const plexTVSignIn = "https://plex.tv/users/sign_in.json"

// Sign-in and verify timeouts are fixed and are not overridable by
// --timeout, $PLEXCTL_TIMEOUT or config `timeout` (contract 2.1,
// exceptions row). auth login runs before there is a usable config, and its
// dial timeout is deliberately just under the overall deadline so a connect
// stall classifies as a dial error rather than racing Client.Timeout. Named
// constants rather than literals so a later reader cannot swap one for
// api.Timeout() without the test noticing.
const (
	signInTimeout     = 15 * time.Second
	signInDialTimeout = 14 * time.Second
	verifyTimeout     = 10 * time.Second
)

// loadOrQuarantineConfig is login's one config read, extracted as a seam:
// Login itself reads stdin and posts to a const plex.tv URL, so it can't be
// tested end-to-end, but this can.
//
// A usable config comes back as-is with no backup. An unusable one — bad
// TOML, or a non-ENOENT read failure — cannot have its unmanaged keys
// carried through the rename-over save, so the loss is made explicit rather
// than silent: the original bytes move to config.toml.corrupt-<RFC3339>,
// the caller warns naming that path, and login continues with an empty map,
// writing only the four managed keys. If the file cannot be moved aside,
// that is INTERNAL (exit 4) — never destroy what could not be backed up.
func loadOrQuarantineConfig() (jsonx.J, string, *output.CLIError) {
	existing, loadErr := config.TryLoad()
	if loadErr == nil {
		return existing, "", nil
	}
	backup := config.Path() + ".corrupt-" + time.Now().UTC().Format(time.RFC3339)
	if err := os.Rename(config.Path(), backup); err != nil {
		return nil, "", output.Err(output.CodeInternal,
			fmt.Sprintf("config at %s is unusable (%v) and could not be moved aside: %v", config.Path(), loadErr, err))
	}
	return jsonx.J{}, backup, nil
}

const authFailedHint = "check credentials and retry: plexctl auth login"

// readSignInBody performs the bounded read of a plex.tv sign-in response and
// the triage that follows it. Extracted as a seam for the same reason
// loadOrQuarantineConfig is: Login reads stdin and posts to a const plex.tv
// URL, so it cannot be driven end to end, and this can.
//
// The order is fixed by contract 2.3: a part-way read failure is a genuine
// transport failure and keeps the cloud target's code; the HTTP status is
// classified next, so an oversize body never converts a 4xx or 5xx into a
// decode error; only then is oversize itself reported, as DECODE_ERROR at
// exit 4 with the bound named.
func readSignInBody(resp *http.Response) ([]byte, *output.CLIError) {
	// ReadBody closes the body on every path, so there is no defer here.
	body, readErr := xhttp.ReadBody(resp, api.BodyLimit)
	oversize := errors.Is(readErr, xhttp.ErrOversize)
	if readErr != nil && !oversize {
		return nil, api.Classify(api.AsError(readErr), api.TargetCloud)
	}
	if resp.StatusCode >= 400 {
		return nil, output.Err(output.CodeAuthFailed,
			fmt.Sprintf("auth failed: HTTP %d", resp.StatusCode)).WithHint(authFailedHint)
	}
	if oversize {
		return nil, api.Classify(api.OversizeError(http.MethodPost, plexTVSignIn), api.TargetCloud)
	}
	return body, nil
}

// tokenFromSignInBody extracts the auth token from a plex.tv sign-in
// response. Extracted for the same reason readSignInBody is: Login cannot be
// driven end to end.
//
// The decode is DecodeOne, not json.Unmarshal — strict single-value decoding
// with UseNumber (contract 2.4, plexctl bullet). The decoder changes; the
// code does not. A non-JSON body, a body that is not an object, and a body
// missing user.authToken are all still PLEX_AUTH_FAILED at exit 2 with the
// credentials hint.
func tokenFromSignInBody(body []byte) (string, *output.CLIError) {
	shapeErr := output.Err(output.CodeAuthFailed, "unexpected auth response shape from plex.tv").WithHint(authFailedHint)
	var payload any
	if err := xhttp.DecodeOne(body, &payload); err != nil {
		return "", output.Err(output.CodeAuthFailed,
			fmt.Sprintf("plex.tv returned non-JSON response: %s", err.Error())).WithHint(authFailedHint)
	}
	payloadMap, ok := payload.(map[string]any)
	if !ok {
		return "", shapeErr
	}
	user, ok := payloadMap["user"].(map[string]any)
	if !ok {
		return "", shapeErr
	}
	tok, ok := user["authToken"].(string)
	if !ok {
		return "", shapeErr
	}
	return tok, nil
}

// mergeConfigPairs overlays the four auth-managed keys onto whatever's
// already in existing (a corrupt or missing config's TryLoad result — see
// its own doc comment on why login must tolerate rather than abort on
// that). Every other key existing already had — the README-documented
// `timeout` included — survives untouched, and with its TOML type intact:
// config.Save now encodes values rather than quoting them, so a numeric
// `timeout = 10` stays an integer instead of coming back as "10".
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
		pairs = append(pairs, config.KV{K: k, V: existing[k]})
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
	// The one config read, up front. It used to be a config.Load() midway
	// through the prompts (below the password), so a corrupt config made
	// login collect a password and then abort on the very file it exists to
	// repair. Reading here also means the merge site downstream reuses this
	// map instead of re-reading the file.
	existing, configBackup, cliErr := loadOrQuarantineConfig()
	if cliErr != nil {
		output.FailErr(cliErr)
		return
	}
	if configBackup != "" {
		fmt.Fprintf(os.Stderr, "Warning: config at %s was unusable and has been moved to %s — only the auth-managed keys will be written; recover anything else from the backup.\n", config.Path(), configBackup)
	}

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
	if v, ok := existing["client_id"]; ok && jsonx.Truthy(v) {
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
	client := api.NewHTTPClient(signInTimeout, &http.Transport{
		DialContext: (&net.Dialer{Timeout: signInDialTimeout}).DialContext,
	})
	resp, err := client.Do(req)
	if err != nil {
		output.FailErr(api.Classify(api.AsError(err), api.TargetCloud))
		return
	}
	body, signInErr := readSignInBody(resp)
	if signInErr != nil {
		output.FailErr(signInErr)
		return
	}

	token, tokenErr := tokenFromSignInBody(body)
	if tokenErr != nil {
		output.FailErr(tokenErr)
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
	verifyClient := api.NewHTTPClient(verifyTimeout, nil)
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
	// onto whatever's already there instead of overwriting it — `existing`
	// being the map loadOrQuarantineConfig read at the top of Login (empty
	// when the old file was quarantined, in which case there is nothing left
	// to preserve).
	pairs := mergeConfigPairs(existing, serverURL, token, defaultClient, clientID)

	// Python's cfg.save() propagates filesystem errors (traceback, exit 1);
	// the Go equivalent is the standard JSON error + exit 1 — never a false
	// "token saved" success.
	if err := config.Save(pairs); err != nil {
		output.FailErr(output.Err(output.CodeInternal, fmt.Sprintf("failed to write config at %s: %s", config.Path(), err.Error())))
		return
	}

	result := jsonx.J{"ok": true, "message": fmt.Sprintf("token saved to %s", config.Path())}
	// Additive, and present only when a config was actually moved aside, so
	// a caller that never hits the corrupt path sees the v1 envelope.
	if configBackup != "" {
		result["configBackup"] = configBackup
	}
	output.PrintOrFail(result)
}
