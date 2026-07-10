package clients

import (
	"testing"

	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/testutil"
)

func TestNormalizeServerList(t *testing.T) {
	list := normalizeServerList([]any{map[string]any{"name": "x"}, "junk"})
	if len(list) != 1 || list[0]["name"] != "x" {
		t.Fatalf("list case: %#v", list)
	}

	single := normalizeServerList(map[string]any{"name": "y"})
	if len(single) != 1 || single[0]["name"] != "y" {
		t.Fatalf("single-object normalization: %#v", single)
	}

	if got := normalizeServerList(nil); len(got) != 0 {
		t.Fatalf("missing Server key: %#v", got)
	}
}

func TestExcludeDevices(t *testing.T) {
	devices := []jsonx.J{
		{"name": "A", "product": "Plex Media Server"},
		{"name": "B", "product": "plexctl"},
		{"name": "C", "product": "Plex for Apple TV"},
		{"name": "D"},                        // missing product -> kept
		{"name": "E", "product": float64(5)}, // non-string product -> kept
	}
	out := excludeDevices(devices)
	var names []string
	for _, d := range out {
		names = append(names, d["name"].(string))
	}
	want := []string{"C", "D", "E"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
}

func activeEntry(name, mid, host string, port float64) jsonx.J {
	e := jsonx.J{"machineIdentifier": mid, "host": host, "port": port}
	if name != "" {
		e["name"] = name
	}
	return e
}

func registeredEntry(name string) jsonx.J {
	d := jsonx.J{"product": "p", "version": "v", "lastSeenAt": "ls"}
	if name != "" {
		d["name"] = name
	}
	return d
}

func TestMergeClientsAmbiguousDuplicates(t *testing.T) {
	active := []jsonx.J{
		activeEntry("Apple TV", "mid-1", "10.0.0.5", 32500),
		activeEntry("Apple TV", "mid-2", "10.0.0.6", 32500),
	}
	registered := []jsonx.J{registeredEntry("Apple TV"), registeredEntry("Apple TV")}

	out := mergeClients(active, registered)
	if len(out) != 2 {
		t.Fatalf("want 2 rows, got %d: %#v", len(out), out)
	}
	for i, row := range out {
		if row["ambiguous"] != true {
			t.Fatalf("row %d: ambiguous = %#v, want true", i, row["ambiguous"])
		}
		if row["machineIdentifier"] != "mid-1" {
			t.Fatalf("row %d: machineIdentifier = %#v, want mid-1 (first active wins)", i, row["machineIdentifier"])
		}
		if row["baseurl"] != "http://10.0.0.5:32500" {
			t.Fatalf("row %d: baseurl = %#v", i, row["baseurl"])
		}
		if row["active"] != true {
			t.Fatalf("row %d: active = %#v, want true", i, row["active"])
		}
	}
}

// TestMergeClientsIPv6HostBracketsBaseurl pins W9: naked "http://"+host+":"+
// port concatenation produced an invalid URL for an IPv6 host (the colons
// in the address collide with the port separator). net.JoinHostPort brackets
// the host correctly.
func TestMergeClientsIPv6HostBracketsBaseurl(t *testing.T) {
	active := []jsonx.J{activeEntry("Apple TV", "mid-1", "fe80::1", 32500)}
	registered := []jsonx.J{registeredEntry("Apple TV")}

	out := mergeClients(active, registered)
	if len(out) != 1 {
		t.Fatalf("want 1 row, got %d: %#v", len(out), out)
	}
	if out[0]["baseurl"] != "http://[fe80::1]:32500" {
		t.Fatalf("baseurl = %#v, want http://[fe80::1]:32500", out[0]["baseurl"])
	}
}

func TestMergeClientsInactiveRegisteredDevice(t *testing.T) {
	active := []jsonx.J{activeEntry("Apple TV", "mid-1", "h", 1)}
	registered := []jsonx.J{registeredEntry("Safari")}

	out := mergeClients(active, registered)
	if len(out) != 1 {
		t.Fatalf("want 1 row, got %d", len(out))
	}
	row := out[0]
	if row["active"] != false {
		t.Fatalf("active = %#v, want false", row["active"])
	}
	if row["machineIdentifier"] != nil {
		t.Fatalf("machineIdentifier = %#v, want nil", row["machineIdentifier"])
	}
	if row["baseurl"] != nil {
		t.Fatalf("baseurl = %#v, want nil", row["baseurl"])
	}
	if row["ambiguous"] != false {
		t.Fatalf("ambiguous = %#v, want false", row["ambiguous"])
	}
}

func TestMergeClientsSkipsNamelessActiveEntries(t *testing.T) {
	active := []jsonx.J{
		activeEntry("", "mid-x", "h", 1),  // no name key
		activeEntry("", "mid-y", "h2", 2), // present but this helper omits empty names too
		activeEntry("Apple TV", "mid-1", "h3", 3),
	}
	// Explicitly cover the "name": "" case distinctly from a missing key.
	active[1]["name"] = ""

	registered := []jsonx.J{registeredEntry("Apple TV")}

	out := mergeClients(active, registered)
	if len(out) != 1 {
		t.Fatalf("want 1 row, got %d", len(out))
	}
	row := out[0]
	if row["active"] != true {
		t.Fatalf("active = %#v, want true", row["active"])
	}
	if row["machineIdentifier"] != "mid-1" {
		t.Fatalf("machineIdentifier = %#v, want mid-1", row["machineIdentifier"])
	}
	if row["ambiguous"] != false {
		t.Fatalf("ambiguous = %#v, want false (only one usable active name)", row["ambiguous"])
	}
}

func TestMergeClientsNamelessRegisteredDevice(t *testing.T) {
	active := []jsonx.J{activeEntry("Apple TV", "mid-1", "h", 1)}
	registered := []jsonx.J{registeredEntry("")}

	out := mergeClients(active, registered)
	if len(out) != 1 {
		t.Fatalf("want 1 row, got %d", len(out))
	}
	row := out[0]
	if row["name"] != nil {
		t.Fatalf("name = %#v, want nil", row["name"])
	}
	if row["active"] != false || row["machineIdentifier"] != nil || row["baseurl"] != nil || row["ambiguous"] != false {
		t.Fatalf("nameless registered device row: %#v", row)
	}
}

// --- resolveIn ---------------------------------------------------------

func resolvedRow(name, mid, baseurl string, active, ambiguous bool) jsonx.J {
	var midVal, baseVal any
	if mid != "" {
		midVal = mid
	}
	if baseurl != "" {
		baseVal = baseurl
	}
	return jsonx.J{
		"name":              name,
		"product":           "p",
		"version":           "v",
		"lastSeen":          "ls",
		"active":            active,
		"machineIdentifier": midVal,
		"baseurl":           baseVal,
		"ambiguous":         ambiguous,
	}
}

func sampleClients() []jsonx.J {
	return []jsonx.J{
		resolvedRow("Apple TV", "mid-1", "http://10.0.0.5:32500", true, true),
		resolvedRow("Apple TV", "mid-1", "http://10.0.0.5:32500", true, true),
		resolvedRow("Safari", "", "", false, false),
		resolvedRow("Mac", "mid-3", "http://10.0.0.9:32500", true, false),
	}
}

func TestResolveInAmbiguousByName(t *testing.T) {
	clientList := sampleClients()
	out, code := testutil.Capture(t, func() {
		resolveIn(clientList, "Apple TV")
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := `{"error":"ambiguous client name 'Apple TV' — multiple active devices share this name; specify by machineIdentifier","ok":false}`
	if got := trimmed(out); got != want {
		t.Fatalf("out = %q, want %q", got, want)
	}
}

func TestResolveInByMachineIdentifierBypassesAmbiguous(t *testing.T) {
	clientList := sampleClients()
	var got jsonx.J
	out, code := testutil.Capture(t, func() {
		got = resolveIn(clientList, "mid-1")
	})
	if code != -1 {
		t.Fatalf("exit code = %d, want -1 (no exit): out=%q", code, out)
	}
	if got["name"] != "Apple TV" || got["machineIdentifier"] != "mid-1" {
		t.Fatalf("resolved = %#v", got)
	}
}

func TestResolveInRegisteredButNotActive(t *testing.T) {
	clientList := sampleClients()
	out, code := testutil.Capture(t, func() {
		resolveIn(clientList, "Safari")
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := `{"error":"'Safari' is registered but not active — open the Plex app","ok":false}`
	if got := trimmed(out); got != want {
		t.Fatalf("out = %q, want %q", got, want)
	}
}

func TestResolveInClientNotFound(t *testing.T) {
	clientList := sampleClients()
	out, code := testutil.Capture(t, func() {
		resolveIn(clientList, "Roku")
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := `{"error":"client not found: Roku","ok":false}`
	if got := trimmed(out); got != want {
		t.Fatalf("out = %q, want %q", got, want)
	}
}

func TestResolveInCaseInsensitivePass(t *testing.T) {
	clientList := sampleClients()
	var got jsonx.J
	out, code := testutil.Capture(t, func() {
		got = resolveIn(clientList, "mac")
	})
	if code != -1 {
		t.Fatalf("exit code = %d, want -1: out=%q", code, out)
	}
	if got["name"] != "Mac" {
		t.Fatalf("resolved = %#v", got)
	}
}

func TestResolveInPass2BailsAmbiguousRegardlessOfMID(t *testing.T) {
	clientList := sampleClients()
	// "apple tv" doesn't exact-match "Apple TV" (pass 1 misses by case, and no
	// mid equals "apple tv"), so this exercises the pass-2 case-insensitive
	// match hitting an ambiguous row and bailing unconditionally.
	out, code := testutil.Capture(t, func() {
		resolveIn(clientList, "apple tv")
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := `{"error":"ambiguous client name 'Apple TV' — multiple active devices share this name; specify by machineIdentifier","ok":false}`
	if got := trimmed(out); got != want {
		t.Fatalf("out = %q, want %q", got, want)
	}
}

func trimmed(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
