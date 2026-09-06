// Package clients ports plexctl/clients.py: merge PMS /clients with plex.tv
// devices.json, joined on lowercased name, with ambiguity flagging and
// print-and-exit resolution.
package clients

import (
	"fmt"
	"net"
	"strings"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/app"
	"github.com/corinthian/plexctl/internal/jsonx"
	"github.com/corinthian/plexctl/internal/output"
)

var excludeProducts = map[string]bool{
	"Plex Media Server": true,
	"plexctl":           true,
}

// normalizeServerList mirrors the MediaContainer.Server quirk in
// _active_clients: PMS may return a single object instead of a list when
// there is exactly one Companion-connected client.
func normalizeServerList(raw any) []jsonx.J {
	switch v := raw.(type) {
	case []any:
		return jsonx.Maps(v)
	case map[string]any:
		return []jsonx.J{v}
	default:
		return nil
	}
}

// activeClients mirrors clients._active_clients: clients currently
// registered with PMS via Companion protocol.
func activeClients() []jsonx.J {
	data := api.Get("/clients", nil)
	mc := jsonx.GetMap(data, "MediaContainer")
	return normalizeServerList(mc["Server"])
}

// excludeDevices drops the plex.tv devices that are never Companion targets
// (the PMS itself, plexctl's own API token) — mirrors _EXCLUDE filtering in
// _registered_devices.
func excludeDevices(devices []jsonx.J) []jsonx.J {
	out := make([]jsonx.J, 0, len(devices))
	for _, d := range devices {
		if p, ok := d["product"].(string); ok && excludeProducts[p] {
			continue
		}
		out = append(out, d)
	}
	return out
}

// registeredDevices mirrors clients._registered_devices: all devices ever
// seen, from the plex.tv account.
func registeredDevices() []jsonx.J {
	v := api.PlexTVGet("/devices.json", nil)
	list, _ := v.([]any)
	return excludeDevices(jsonx.Maps(list))
}

// normName mirrors clients._norm_name.
func normName(v any) (string, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return "", false
	}
	return strings.ToLower(s), true
}

// baseURL builds the Companion base URL for an active client. net.JoinHostPort
// brackets an IPv6 host, whose own colons would otherwise collide with the
// port separator.
func baseURL(ac jsonx.J) string {
	return "http://" + net.JoinHostPort(jsonx.AsStr(ac["host"]), jsonx.AsStr(ac["port"]))
}

// mergeClients joins plex.tv's registered devices onto the Companion clients
// PMS currently reports, by lowercased name.
//
// The output is keyed on active identity, not on the registered list. Two
// devices sharing a name used to collapse onto the first one's
// machineIdentifier — every row carried it, so the second device was
// unaddressable and PLEX_CLIENT_AMBIGUOUS listed the one identifier it had
// kept while telling the caller to target by identifier. A same-named pair
// now yields one row per active device, each with its own identifier and
// baseurl, all flagged ambiguous.
//
// One row per *active* device, deliberately not one per registered row:
// without a stable identifier on the plex.tv side there is no way to pair N
// registered rows to N same-named actives, so the registered rows beyond the
// first are folded into that enumeration rather than duplicating it. The
// registered row supplies the descriptive fields (name/product/version/
// lastSeen) because there is nothing better to attribute them from.
//
// An active device plex.tv has no row for gets a synthetic row instead of
// vanishing: it is reachable right now, so it has to be listable.
func mergeClients(active []jsonx.J, registered []jsonx.J) []jsonx.J {
	activesByName := map[string][]jsonx.J{}
	var activeNames []string // first-sight order; map iteration isn't stable
	for _, c := range active {
		k, ok := normName(c["name"])
		if !ok {
			continue
		}
		if _, seen := activesByName[k]; !seen {
			activeNames = append(activeNames, k)
		}
		activesByName[k] = append(activesByName[k], c)
	}

	joined := func(d jsonx.J, ac jsonx.J, ambiguous bool) jsonx.J {
		row := jsonx.J{
			"name":              d["name"],
			"product":           d["product"],
			"version":           d["version"],
			"lastSeen":          d["lastSeenAt"],
			"active":            ac != nil,
			"machineIdentifier": nil,
			"baseurl":           nil,
			"ambiguous":         ambiguous,
		}
		if ac != nil {
			row["machineIdentifier"] = ac["machineIdentifier"]
			row["baseurl"] = baseURL(ac)
		}
		return row
	}

	out := make([]jsonx.J, 0, len(registered))
	matched := map[string]bool{}    // names covered by some registered row
	enumerated := map[string]bool{} // ambiguous names already expanded
	for _, d := range registered {
		var acs []jsonx.J
		k, ok := normName(d["name"])
		if ok {
			acs = activesByName[k]
			if len(acs) > 0 {
				matched[k] = true
			}
		}
		switch {
		case len(acs) == 0:
			out = append(out, joined(d, nil, false))
		case len(acs) == 1:
			out = append(out, joined(d, acs[0], false))
		default:
			if enumerated[k] {
				continue
			}
			enumerated[k] = true
			for _, ac := range acs {
				out = append(out, joined(d, ac, true))
			}
		}
	}

	for _, k := range activeNames {
		if matched[k] {
			continue
		}
		acs := activesByName[k]
		for _, ac := range acs {
			out = append(out, jsonx.J{
				"name":              ac["name"],
				"product":           ac["product"],
				"version":           ac["version"],
				"lastSeen":          nil, // plex.tv has no row to date it
				"active":            true,
				"machineIdentifier": ac["machineIdentifier"],
				"baseurl":           baseURL(ac),
				"ambiguous":         len(acs) > 1,
			})
		}
	}
	return out
}

// ListClients mirrors clients.list_clients.
func ListClients() []jsonx.J {
	return mergeClients(activeClients(), registeredDevices())
}

// PrintClients mirrors clients.print_clients. The note's denominator is the
// merged row count, which since the identifier-retaining merge includes
// active devices plex.tv has no row for and one row per device behind an
// ambiguous name — so both halves of the ratio can differ from what a
// pre-fix binary printed for the same network.
func PrintClients() {
	clientList := ListClients()
	active := 0
	for _, c := range clientList {
		if b, _ := c["active"].(bool); b {
			active++
		}
	}
	output.PrintOrFail(jsonx.J{
		"ok":      true,
		"clients": clientList,
		"note":    fmt.Sprintf("%d/%d clients currently controllable (app must be open)", active, len(clientList)),
	})
}

// bailAmbiguous reports every device sharing the ambiguous name, not just
// the row resolution happened to land on: the hint tells the caller to
// target by machineIdentifier, so the envelope has to carry all of them.
func bailAmbiguous(clientList []jsonx.J, c jsonx.J) jsonx.J {
	msg := fmt.Sprintf("ambiguous client name '%s' — multiple active devices share this name; specify by machineIdentifier", jsonx.AsStr(c["name"]))
	wanted, _ := normName(c["name"])
	matches := []jsonx.J{}
	for _, row := range clientList {
		if k, ok := normName(row["name"]); !ok || k != wanted {
			continue
		}
		if ambiguous, _ := row["ambiguous"].(bool); !ambiguous {
			continue
		}
		matches = append(matches, jsonx.J{"name": row["name"], "machineIdentifier": row["machineIdentifier"]})
	}
	output.FailErr(output.Err(output.CodeClientAmbiguous, msg).
		WithHint("target by machineIdentifier — run: plexctl clients").
		WithData("matches", matches))
	return jsonx.J{} // reached only when output.Exit is a test seam
}

// resolveIn mirrors clients.resolve's matching logic against an
// already-computed client list, so it is testable without a network round
// trip.
func resolveIn(clientList []jsonx.J, target string) jsonx.J {
	for _, c := range clientList {
		nameStr, nameIsStr := c["name"].(string)
		midStr, midIsStr := c["machineIdentifier"].(string)
		if (nameIsStr && nameStr == target) || (midIsStr && midStr == target) {
			ambiguous, _ := c["ambiguous"].(bool)
			if ambiguous && !(midIsStr && midStr == target) {
				return bailAmbiguous(clientList, c)
			}
			active, _ := c["active"].(bool)
			if !active {
				output.FailErr(output.Err(output.CodeClientInactive, fmt.Sprintf("'%s' is registered but not active — open the Plex app", target)).
					WithHint("open (or relaunch) Plex on the device, then retry").
					WithData("client", target))
				return jsonx.J{} // reached only when output.Exit is a test seam
			}
			return c
		}
	}

	targetLower := strings.ToLower(target)
	for _, c := range clientList {
		cname, ok := c["name"].(string)
		if !ok || strings.ToLower(cname) != targetLower {
			continue
		}
		ambiguous, _ := c["ambiguous"].(bool)
		if ambiguous {
			return bailAmbiguous(clientList, c)
		}
		active, _ := c["active"].(bool)
		if !active {
			output.FailErr(output.Err(output.CodeClientInactive, fmt.Sprintf("'%s' is registered but not active — open the Plex app", target)).
				WithHint("open (or relaunch) Plex on the device, then retry").
				WithData("client", target))
			return jsonx.J{} // reached only when output.Exit is a test seam
		}
		return c
	}

	output.FailErr(output.Err(output.CodeClientUnknown, fmt.Sprintf("client not found: %s", target)).
		WithHint("run: plexctl clients").
		WithData("client", target))
	return jsonx.J{} // reached only when output.Exit is a test seam
}

// Resolve mirrors clients.resolve: returns the active client dict
// {machineIdentifier, baseurl, name, ...} or prints the error and exits.
// name == "" means "use default_client from config".
func Resolve(name string) jsonx.J {
	target := name
	if target == "" {
		target = app.Current().Require("default_client")
	}
	return resolveIn(ListClients(), target)
}
