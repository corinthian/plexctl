package clients

import (
	"strings"
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

// TestMergeClientsAmbiguousDuplicates used to pin the defect — "first active
// wins", both rows carrying mid-1. Inverted: the rows are now one per active
// device, in the order PMS reported them, each keeping its own identity.
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
	wantMID := []string{"mid-1", "mid-2"}
	wantBase := []string{"http://10.0.0.5:32500", "http://10.0.0.6:32500"}
	for i, row := range out {
		if row["ambiguous"] != true {
			t.Fatalf("row %d: ambiguous = %#v, want true", i, row["ambiguous"])
		}
		if row["machineIdentifier"] != wantMID[i] {
			t.Fatalf("row %d: machineIdentifier = %#v, want %s", i, row["machineIdentifier"], wantMID[i])
		}
		if row["baseurl"] != wantBase[i] {
			t.Fatalf("row %d: baseurl = %#v, want %s", i, row["baseurl"], wantBase[i])
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

	// Two rows now: the registered-but-inactive Safari, plus a synthetic row
	// for the active Apple TV plex.tv did not list (see
	// TestMergeClientsKeepsActiveAbsentFromPlexTV). This case is about Safari.
	out := mergeClients(active, registered)
	if len(out) != 2 {
		t.Fatalf("want 2 rows, got %d: %#v", len(out), out)
	}
	row := out[0]
	if row["name"] != "Safari" {
		t.Fatalf("row 0 = %#v, want the registered Safari row first", row)
	}
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

	// The nameless registered row can join nothing, and the active Apple TV
	// it cannot be matched to still gets its own synthetic row.
	out := mergeClients(active, registered)
	if len(out) != 2 {
		t.Fatalf("want 2 rows, got %d: %#v", len(out), out)
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
		// Post-merge shape: same-named actives keep distinct identities.
		resolvedRow("Apple TV", "mid-1", "http://10.0.0.5:32500", true, true),
		resolvedRow("Apple TV", "mid-2", "http://10.0.0.6:32500", true, true),
		resolvedRow("Safari", "", "", false, false),
		resolvedRow("Mac", "mid-3", "http://10.0.0.9:32500", true, false),
	}
}

func TestResolveInAmbiguousByName(t *testing.T) {
	// v2 (docs/error_model_v2.md): ambiguous client name -> PLEX_CLIENT_AMBIGUOUS,
	// exit 2, structured envelope with matches under data.
	clientList := sampleClients()
	out, code := testutil.Capture(t, func() {
		resolveIn(clientList, "Apple TV")
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(out, `"code":"PLEX_CLIENT_AMBIGUOUS"`) {
		t.Fatalf("code drifted: %q", out)
	}
	if !strings.Contains(out, `"message":"ambiguous client name 'Apple TV' — multiple active devices share this name; specify by machineIdentifier"`) {
		t.Fatalf("message drifted: %q", out)
	}
	if !strings.Contains(out, `"hint":"target by machineIdentifier — run: plexctl clients"`) {
		t.Fatalf("hint drifted: %q", out)
	}
	if !strings.Contains(out, `"data":{"matches":[{"machineIdentifier":"mid-1","name":"Apple TV"},{"machineIdentifier":"mid-2","name":"Apple TV"}]}`) {
		t.Fatalf("data drifted: %q", out)
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
	// v2: registered-but-inactive -> PLEX_CLIENT_INACTIVE, exit 2.
	clientList := sampleClients()
	out, code := testutil.Capture(t, func() {
		resolveIn(clientList, "Safari")
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(out, `"code":"PLEX_CLIENT_INACTIVE"`) {
		t.Fatalf("code drifted: %q", out)
	}
	if !strings.Contains(out, `"message":"'Safari' is registered but not active — open the Plex app"`) {
		t.Fatalf("message drifted: %q", out)
	}
	if !strings.Contains(out, `"hint":"open (or relaunch) Plex on the device, then retry"`) {
		t.Fatalf("hint drifted: %q", out)
	}
	if !strings.Contains(out, `"data":{"client":"Safari"}`) {
		t.Fatalf("data drifted: %q", out)
	}
}

func TestResolveInClientNotFound(t *testing.T) {
	// v2: no match at all -> PLEX_CLIENT_UNKNOWN, exit 2.
	clientList := sampleClients()
	out, code := testutil.Capture(t, func() {
		resolveIn(clientList, "Roku")
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(out, `"code":"PLEX_CLIENT_UNKNOWN"`) {
		t.Fatalf("code drifted: %q", out)
	}
	if !strings.Contains(out, `"message":"client not found: Roku"`) {
		t.Fatalf("message drifted: %q", out)
	}
	if !strings.Contains(out, `"hint":"run: plexctl clients"`) {
		t.Fatalf("hint drifted: %q", out)
	}
	if !strings.Contains(out, `"data":{"client":"Roku"}`) {
		t.Fatalf("data drifted: %q", out)
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
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(out, `"code":"PLEX_CLIENT_AMBIGUOUS"`) {
		t.Fatalf("code drifted: %q", out)
	}
	if !strings.Contains(out, `"message":"ambiguous client name 'Apple TV' — multiple active devices share this name; specify by machineIdentifier"`) {
		t.Fatalf("message drifted: %q", out)
	}
	if !strings.Contains(out, `"data":{"matches":[{"machineIdentifier":"mid-1","name":"Apple TV"},{"machineIdentifier":"mid-2","name":"Apple TV"}]}`) {
		t.Fatalf("data drifted: %q", out)
	}
}

// TestMergeClientsRetainsEveryActiveIdentifier pins the item-3 fix. Two
// active devices sharing a name used to collapse onto the first one's
// machineIdentifier: every row carried mid-A, so the second device was
// unaddressable — and PLEX_CLIENT_AMBIGUOUS told the caller to "specify by
// machineIdentifier" while listing only the one identifier it had kept. The
// merge now emits one row per active device, each with its own identity.
func TestMergeClientsRetainsEveryActiveIdentifier(t *testing.T) {
	active := []jsonx.J{
		activeEntry("TV", "A", "10.0.0.5", 32500),
		activeEntry("TV", "B", "10.0.0.6", 32500),
	}
	registered := []jsonx.J{registeredEntry("TV"), registeredEntry("TV")}

	out := mergeClients(active, registered)
	byMID := map[string]jsonx.J{}
	for _, row := range out {
		mid, _ := row["machineIdentifier"].(string)
		byMID[mid] = row
	}
	for _, mid := range []string{"A", "B"} {
		row, ok := byMID[mid]
		if !ok {
			t.Fatalf("no row for machineIdentifier %q: %#v", mid, out)
		}
		if row["ambiguous"] != true || row["active"] != true {
			t.Fatalf("row %q: ambiguous=%#v active=%#v, want true/true", mid, row["ambiguous"], row["active"])
		}
	}
	if byMID["A"]["baseurl"] != "http://10.0.0.5:32500" || byMID["B"]["baseurl"] != "http://10.0.0.6:32500" {
		t.Fatalf("each ambiguous row must keep its own baseurl: %#v", out)
	}
	// Two same-named actives are two rows, not one per registered row:
	// pairing N registered rows to N identifier-less actives is unsolvable
	// and would rebuild the same defect.
	if len(out) != 2 {
		t.Fatalf("want 2 rows (one per active device), got %d: %#v", len(out), out)
	}

	// The recommended recovery — target the machineIdentifier the error
	// listed — has to resolve.
	var got jsonx.J
	capOut, code := testutil.Capture(t, func() { got = resolveIn(out, "B") })
	if code != -1 {
		t.Fatalf("resolveIn(rows, \"B\") exited %d: %s", code, capOut)
	}
	if got["machineIdentifier"] != "B" || got["baseurl"] != "http://10.0.0.6:32500" {
		t.Fatalf("resolved = %#v, want the B device", got)
	}
}

// TestMergeClientsKeepsActiveAbsentFromPlexTV covers the other half of the
// same defect: the merge iterated registered devices only, so a client PMS
// reports as active but plex.tv has no row for vanished from `clients`
// entirely — unlistable and therefore untargetable.
func TestMergeClientsKeepsActiveAbsentFromPlexTV(t *testing.T) {
	active := []jsonx.J{activeEntry("Kitchen", "mid-k", "10.0.0.7", 32500)}
	registered := []jsonx.J{registeredEntry("Safari")}

	out := mergeClients(active, registered)
	if len(out) != 2 {
		t.Fatalf("want 2 rows (registered Safari + synthetic Kitchen), got %d: %#v", len(out), out)
	}
	var synthetic jsonx.J
	for _, row := range out {
		if row["name"] == "Kitchen" {
			synthetic = row
		}
	}
	if synthetic == nil {
		t.Fatalf("no row for the active-but-unregistered device: %#v", out)
	}
	if synthetic["active"] != true || synthetic["machineIdentifier"] != "mid-k" ||
		synthetic["baseurl"] != "http://10.0.0.7:32500" || synthetic["ambiguous"] != false {
		t.Fatalf("synthetic row = %#v", synthetic)
	}
	if synthetic["lastSeen"] != nil {
		t.Fatalf("lastSeen = %#v, want nil — plex.tv has no row to date it", synthetic["lastSeen"])
	}
}
