// Package auth ports plexctl/auth.py: interactive plex.tv sign-in, PMS
// reachability check, config write.
package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/config"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/output"
)

const plexTVSignIn = "https://plex.tv/users/sign_in.json"

// randomClientIDSuffix mirrors uuid.uuid4().hex[:8]: 8 lowercase hex chars.
func randomClientIDSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// classifyAuthTransport mirrors auth.py's exception ladder, which — unlike
// api.py's — catches ConnectionError BEFORE Timeout. requests'
// ConnectTimeout subclasses ConnectionError, so a connect-phase timeout is
// "connection failed" here; only read timeouts are "auth request timed out".
// The sign-in client uses a bounded dialer so dial-phase stalls surface as
// dial op-errors rather than the phase-blind Client.Timeout error.
func classifyAuthTransport(err error) string {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return "connection failed: " + err.Error()
	}
	var ne net.Error
	if (errors.As(err, &ne) && ne.Timeout()) || errors.Is(err, context.DeadlineExceeded) {
		return "auth request timed out: " + err.Error()
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return "connection failed: " + err.Error()
	}
	return "auth request failed: " + err.Error()
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

	req, err := http.NewRequest(http.MethodPost, plexTVSignIn, nil)
	if err != nil {
		output.Fail("auth request failed: " + err.Error())
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
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{Timeout: 14 * time.Second}).DialContext,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		output.Fail(classifyAuthTransport(err))
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		output.Fail(classifyAuthTransport(err))
		return
	}
	if resp.StatusCode >= 400 {
		output.Fail(fmt.Sprintf("auth failed: HTTP %d", resp.StatusCode))
		return
	}

	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		output.Fail(fmt.Sprintf("plex.tv returned non-JSON response: %s", err.Error()))
		return
	}
	payloadMap, ok := payload.(map[string]any)
	if !ok {
		output.Fail("unexpected auth response shape from plex.tv")
		return
	}
	user, ok := payloadMap["user"].(map[string]any)
	if !ok {
		output.Fail("unexpected auth response shape from plex.tv")
		return
	}
	token, ok := user["authToken"].(string)
	if !ok {
		output.Fail("unexpected auth response shape from plex.tv")
		return
	}

	// Verify PMS is reachable before writing config
	verifyReq, err := http.NewRequest(http.MethodGet, strings.TrimRight(serverURL, "/")+"/", nil)
	if err != nil {
		output.Fail(fmt.Sprintf("PMS unreachable at %s: %s", serverURL, err.Error()))
		return
	}
	for k, v := range headers {
		verifyReq.Header.Set(k, v)
	}
	verifyReq.Header.Set("X-Plex-Token", token)
	verifyClient := &http.Client{Timeout: 10 * time.Second}
	verifyResp, err := verifyClient.Do(verifyReq)
	if err != nil {
		output.Fail(fmt.Sprintf("PMS unreachable at %s: %s", serverURL, err.Error()))
		return
	}
	defer verifyResp.Body.Close()
	if verifyResp.StatusCode >= 400 {
		output.Fail(fmt.Sprintf("PMS unreachable at %s: %d %s", serverURL, verifyResp.StatusCode, http.StatusText(verifyResp.StatusCode)))
		return
	}

	// Python's cfg.save() propagates filesystem errors (traceback, exit 1);
	// the Go equivalent is the standard JSON error + exit 1 — never a false
	// "token saved" success.
	if err := config.Save([]config.KV{
		{K: "server_url", V: serverURL},
		{K: "token", V: token},
		{K: "default_client", V: defaultClient},
		{K: "client_id", V: clientID},
	}); err != nil {
		output.Fail(fmt.Sprintf("failed to write config at %s: %s", config.Path(), err.Error()))
		return
	}

	output.Print(jsonx.J{"ok": true, "message": fmt.Sprintf("token saved to %s", config.Path())})
}
