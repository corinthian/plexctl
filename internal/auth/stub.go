// Package auth ports plexctl/auth.py: interactive plex.tv sign-in, PMS
// reachability check, config write.
package auth

// The blank import pins x/term (hidden password input) in go.mod so the
// domain port never needs to touch module files.
import _ "golang.org/x/term"

// Login mirrors auth.login (interactive; prints JSON result or error+exit).
func Login() { panic("not ported: auth.Login") }
