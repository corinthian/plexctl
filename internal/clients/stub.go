// Package clients ports plexctl/clients.py: merge PMS /clients with plex.tv
// devices.json, joined on lowercased name, with ambiguity flagging and
// print-and-exit resolution.
package clients

import "github.com/corinthian/plexctl/internal/jsonx"

// ListClients mirrors clients.list_clients.
func ListClients() []jsonx.J { panic("not ported: clients.ListClients") }

// PrintClients mirrors clients.print_clients.
func PrintClients() { panic("not ported: clients.PrintClients") }

// Resolve mirrors clients.resolve: returns the active client dict
// {machineIdentifier, baseurl, name, ...} or prints the error and exits.
// name == "" means "use default_client from config".
func Resolve(name string) jsonx.J { panic("not ported: clients.Resolve") }
