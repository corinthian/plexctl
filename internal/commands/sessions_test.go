package commands_test

import (
	"testing"

	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/testutil"
)

func TestHistoryLimitBelowOneIsUsageError(t *testing.T) {
	_ = newFakePMS(t)

	for _, limit := range []string{"0", "-5"} {
		t.Run("limit="+limit, func(t *testing.T) {
			root := commands.BuildRoot()
			root.SetArgs([]string{"history", "--limit", limit})
			var err error
			out, code := testutil.Capture(t, func() { err = root.Execute() })
			if err == nil {
				t.Fatalf("expected a usage error for --limit %s, got nil", limit)
			}
			if code != -1 {
				t.Fatalf("exit = %d, want -1 (usage error never reaches output.Exit); out=%s", code, out)
			}
		})
	}
}

func TestHistoryLimitOneIsAccepted(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/status/sessions/history/all", map[string]any{"MediaContainer": map[string]any{}})

	root := commands.BuildRoot()
	root.SetArgs([]string{"history", "--limit", "1"})
	out, code := testutil.Capture(t, func() { _ = root.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1 (ok:true, no exit); out=%s", code, out)
	}
	got := mustUnmarshal(t, out)
	if got["ok"] != true {
		t.Fatalf("ok = %#v, out=%s", got["ok"], out)
	}
}

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
