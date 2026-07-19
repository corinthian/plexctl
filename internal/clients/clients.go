// Package clients ports plexctl/clients.py: merge PMS /clients with plex.tv
// devices.json, joined on lowercased name, with ambiguity flagging and
// print-and-exit resolution.
package clients

import (
	"fmt"
	"net"
	"strings"

	"github.com/corinthian/plexctl/internal/api"
	"github.com/corinthian/plexctl/internal/config"
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

// mergeClients mirrors clients.list_clients' merge step, joining registered
// devices onto active Companion clients by lowercased name. A second active
// client sharing a name marks that name ambiguous; the by-name map still
// holds the first active client, so ambiguous rows carry its
// machineIdentifier.
func mergeClients(active []jsonx.J, registered []jsonx.J) []jsonx.J {
	activeByName := map[string]jsonx.J{}
	duplicateNames := map[string]bool{}
	for _, c := range active {
		k, ok := normName(c["name"])
		if !ok {
			continue
		}
		if _, exists := activeByName[k]; exists {
			duplicateNames[k] = true
			continue
		}
		activeByName[k] = c
	}

	out := make([]jsonx.J, 0, len(registered))
	for _, d := range registered {
		k, ok := normName(d["name"])
		var ac jsonx.J
		if ok {
			ac = activeByName[k]
		}
		row := jsonx.J{
			"name":              d["name"],
			"product":           d["product"],
			"version":           d["version"],
			"lastSeen":          d["lastSeenAt"],
			"active":            ac != nil,
			"machineIdentifier": nil,
			"baseurl":           nil,
			"ambiguous":         false,
		}
		if ac != nil {
			row["machineIdentifier"] = ac["machineIdentifier"]
			row["baseurl"] = "http://" + net.JoinHostPort(jsonx.AsStr(ac["host"]), jsonx.AsStr(ac["port"]))
		}
		if ok {
			row["ambiguous"] = duplicateNames[k]
		}
		out = append(out, row)
	}
	return out
}

// ListClients mirrors clients.list_clients.
func ListClients() []jsonx.J {
	return mergeClients(activeClients(), registeredDevices())
}

// PrintClients mirrors clients.print_clients.
func PrintClients() {
	clientList := ListClients()
	active := 0
	for _, c := range clientList {
		if b, _ := c["active"].(bool); b {
			active++
		}
	}
	output.Print(jsonx.J{
		"ok":      true,
		"clients": clientList,
		"note":    fmt.Sprintf("%d/%d clients currently controllable (app must be open)", active, len(clientList)),
	})
}

func bailAmbiguous(c jsonx.J) jsonx.J {
	msg := fmt.Sprintf("ambiguous client name '%s' — multiple active devices share this name; specify by machineIdentifier", jsonx.AsStr(c["name"]))
	matches := []jsonx.J{{"name": c["name"], "machineIdentifier": c["machineIdentifier"]}}
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
				return bailAmbiguous(c)
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
			return bailAmbiguous(c)
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
		target = config.Require("default_client")
	}
	return resolveIn(ListClients(), target)
}
