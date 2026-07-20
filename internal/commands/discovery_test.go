package commands_test

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/corinthian/plexctl/internal/commands"
	"github.com/corinthian/plexctl/internal/testutil"
)

// flattenNames walks a discovery node (map[string]any per the JSON shape) and
// collects every "name" in the tree, including the root.
func flattenNames(t *testing.T, node map[string]any, out map[string]bool) {
	t.Helper()
	name, ok := node["name"].(string)
	if !ok {
		t.Fatalf("node missing string \"name\": %#v", node)
	}
	out[name] = true
	if _, ok := node["path"].(string); !ok {
		t.Fatalf("node %q missing string \"path\": %#v", name, node)
	}
	if _, hasSummary := node["summary"]; !hasSummary {
		t.Fatalf("node %q missing \"summary\" key: %#v", name, node)
	}
	// Exactly the four keys name/path/summary/subcommands (subcommands
	// omitted on leaves) — catch any stray key that would drift from
	// traktctl's commandNode shape.
	allowed := map[string]bool{"name": true, "path": true, "summary": true, "subcommands": true}
	for k := range node {
		if !allowed[k] {
			t.Fatalf("node %q has unexpected key %q: %#v", name, k, node)
		}
	}
	subs, ok := node["subcommands"].([]any)
	if !ok {
		return
	}
	for _, s := range subs {
		child, ok := s.(map[string]any)
		if !ok {
			t.Fatalf("subcommand entry is not an object: %#v", s)
		}
		flattenNames(t, child, out)
	}
}

// TestCommandsDiscoveryListsEveryRegisteredCommand walks the live cobra tree
// directly and cross-checks it against the `commands` command's JSON output:
// every non-hidden, non-help, non-completion command must appear by name,
// and "help"/"completion" (and anything Hidden) must be absent.
func TestCommandsDiscoveryListsEveryRegisteredCommand(t *testing.T) {
	_ = newFakePMS(t)
	root := commands.BuildRoot()

	var wantNames []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, child := range c.Commands() {
			if child.Hidden || child.Name() == "help" || child.Name() == "completion" {
				continue
			}
			wantNames = append(wantNames, child.Name())
			walk(child)
		}
	}
	walk(root)
	if len(wantNames) == 0 {
		t.Fatal("expected at least one registered command in the live tree")
	}

	root2 := commands.BuildRoot()
	root2.SetArgs([]string{"commands"})
	out, code := testutil.Capture(t, func() { _ = root2.Execute() })
	if code != -1 {
		t.Fatalf("exit = %d, want -1 (ok:true, no exit); out=%s", code, out)
	}

	got := mustUnmarshal(t, out)
	if got["ok"] != true {
		t.Fatalf("ok = %#v, want true; out=%s", got["ok"], out)
	}
	tree, ok := got["commands"].(map[string]any)
	if !ok {
		t.Fatalf("commands = %#v, want an object; out=%s", got["commands"], out)
	}
	if tree["name"] != "plexctl" {
		t.Fatalf("root name = %#v, want plexctl", tree["name"])
	}

	seen := map[string]bool{}
	flattenNames(t, tree, seen)

	for _, name := range wantNames {
		if !seen[name] {
			t.Errorf("command %q missing from discovery tree", name)
		}
	}
	for _, hidden := range []string{"help", "completion"} {
		if seen[hidden] {
			t.Errorf("discovery tree must not include %q", hidden)
		}
	}
}
