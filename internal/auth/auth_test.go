package auth

import (
	"reflect"
	"testing"

	"github.com/corinthian/plexctl/internal/config"
	"github.com/corinthian/plexctl/internal/jsonx"
)

// TestMergeConfigPairsPreservesHandAddedKey pins W5: auth login used to
// Save only its own four keys, silently destroying any other key the
// config already had — the README-documented `timeout` included.
func TestMergeConfigPairsPreservesHandAddedKey(t *testing.T) {
	existing := jsonx.J{"timeout": int64(10)}
	pairs := mergeConfigPairs(existing, "http://pms:32400", "tok", "Apple TV", "cid-1")

	want := []config.KV{
		{K: "timeout", V: "10"},
		{K: "server_url", V: "http://pms:32400"},
		{K: "token", V: "tok"},
		{K: "default_client", V: "Apple TV"},
		{K: "client_id", V: "cid-1"},
	}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("pairs = %#v, want %#v", pairs, want)
	}
}

// TestMergeConfigPairsManagedKeysAlwaysOverwritten proves the four
// auth-managed keys always take the freshly authenticated values, even
// when the existing config already had (stale) values for them.
func TestMergeConfigPairsManagedKeysAlwaysOverwritten(t *testing.T) {
	existing := jsonx.J{
		"server_url":     "http://stale:32400",
		"token":          "stale-token",
		"default_client": "Old Client",
		"client_id":      "stale-cid",
		"timeout":        "30",
	}
	pairs := mergeConfigPairs(existing, "http://fresh:32400", "fresh-token", "Apple TV", "fresh-cid")

	want := []config.KV{
		{K: "timeout", V: "30"},
		{K: "server_url", V: "http://fresh:32400"},
		{K: "token", V: "fresh-token"},
		{K: "default_client", V: "Apple TV"},
		{K: "client_id", V: "fresh-cid"},
	}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("pairs = %#v, want %#v", pairs, want)
	}
}

// TestMergeConfigPairsEmptyExisting covers first-ever login: no prior
// config, only the four managed keys are written.
func TestMergeConfigPairsEmptyExisting(t *testing.T) {
	pairs := mergeConfigPairs(jsonx.J{}, "http://pms:32400", "tok", "Apple TV", "cid-1")

	want := []config.KV{
		{K: "server_url", V: "http://pms:32400"},
		{K: "token", V: "tok"},
		{K: "default_client", V: "Apple TV"},
		{K: "client_id", V: "cid-1"},
	}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("pairs = %#v, want %#v", pairs, want)
	}
}

// TestValidatePMSURL pins W5 (finding 2, salvaged parts): reject any scheme
// other than http/https, userinfo, fragments, and query strings before the
// URL is ever used on the network.
func TestValidatePMSURL(t *testing.T) {
	rejects := []string{
		"ftp://pms.example:32400",             // wrong scheme
		"http://user:pass@pms.example:32400",  // userinfo
		"http:///just/a/path",                 // hostless
		"http://pms.example:32400#fragment",   // fragment
		"http://pms.example:32400?token=leak", // query string
		"not a url at all",                    // unparseable / no scheme
	}
	for _, raw := range rejects {
		t.Run("reject "+raw, func(t *testing.T) {
			if _, err := validatePMSURL(raw); err == nil {
				t.Fatalf("validatePMSURL(%q) = nil error, want a rejection", raw)
			}
		})
	}

	accepts := []string{
		"http://pms.example:32400",
		"https://pms.example:32400",
		"http://pms.example:32400/",
		"https://10.0.0.5:32400",
	}
	for _, raw := range accepts {
		t.Run("accept "+raw, func(t *testing.T) {
			if _, err := validatePMSURL(raw); err != nil {
				t.Fatalf("validatePMSURL(%q) = %v, want no error", raw, err)
			}
		})
	}
}
