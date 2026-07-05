package commands_test

import (
	"testing"

	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/testutil"
)

func TestContextNoHistoryOmitsHistoryKey(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/status/sessions", map[string]any{"MediaContainer": map[string]any{}})

	root := commands.BuildRoot()
	root.SetArgs([]string{"context", "--no-history"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1 (ok:true, no exit); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != true {
		t.Fatalf("ok = %#v, out=%s", got["ok"], out)
	}
	if _, exists := got["history"]; exists {
		t.Fatalf("history key present with --no-history: %#v", got)
	}
}

func TestContextIncludesHistoryByDefault(t *testing.T) {
	f := newFakePMS(t)
	f.resolvableClient(t)
	f.onJSON("GET", "/status/sessions", map[string]any{"MediaContainer": map[string]any{}})
	f.onJSON("GET", "/status/sessions/history/all", map[string]any{"MediaContainer": map[string]any{}})

	root := commands.BuildRoot()
	root.SetArgs([]string{"context"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1; out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if _, exists := got["history"]; !exists {
		t.Fatalf("expected history key present by default: %#v", got)
	}
}
