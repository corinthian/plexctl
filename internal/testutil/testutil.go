// Package testutil standardizes the port's test setup: a redirected config
// dir pointed at an httptest server, and capture of the print-and-exit
// control flow via the output seams.
package testutil

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/corinthian/plexctl/internal/output"
)

// Setup points PLEXCTL_CONFIG_DIR at a fresh temp dir whose config.toml aims
// plexctl at serverURL. Returns the config dir (queue_state.json lands there
// too).
func Setup(t *testing.T, serverURL string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := "server_url = \"" + serverURL + "\"\n" +
		"token = \"test-token\"\n" +
		"default_client = \"Apple TV\"\n" +
		"client_id = \"plexctl-test\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLEXCTL_CONFIG_DIR", dir)
	return dir
}

// ExitPanic carries the simulated exit code through the stack.
type ExitPanic struct{ Code int }

// Capture runs fn with output.Stdout captured and output.Exit replaced by a
// panic, so print-and-exit paths stop where os.Exit would. Returns everything
// printed and the exit code (-1 when fn returned without exiting).
func Capture(t *testing.T, fn func()) (out string, code int) {
	t.Helper()
	var buf bytes.Buffer
	oldW, oldE := output.Stdout, output.Exit
	output.Stdout = &buf
	output.Exit = func(c int) { panic(ExitPanic{c}) }
	defer func() {
		output.Stdout, output.Exit = oldW, oldE
		out = buf.String()
		if r := recover(); r != nil {
			ep, ok := r.(ExitPanic)
			if !ok {
				panic(r)
			}
			code = ep.Code
		}
	}()
	code = -1
	fn()
	return
}
