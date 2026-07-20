package queue

import (
	"reflect"
	"testing"
	"time"

	"github.com/corinthian/plexctl/internal/jsonx"
)

// fastVerify compresses the polling window and counts sleeps so tests can
// assert both outcome and pacing without real waits.
func fastVerify(t *testing.T, probes int) *int {
	t.Helper()
	oldProbes, oldSleep := VerifyProbes, VerifySleep
	slept := 0
	VerifyProbes = probes
	VerifySleep = func(time.Duration) { slept++ }
	t.Cleanup(func() { VerifyProbes, VerifySleep = oldProbes, oldSleep })
	return &slept
}

func sessionsResp(mid, state, ratingKey string) jsonx.J {
	return jsonx.J{"MediaContainer": jsonx.J{"Metadata": []any{
		jsonx.J{"ratingKey": ratingKey, "Player": jsonx.J{"machineIdentifier": mid, "state": state}},
	}}}
}

func TestConfirmEngagedMatchesAllowedKeyFirstProbeNoSleep(t *testing.T) {
	f := newFakePMS(t)
	slept := fastVerify(t, 3)
	f.onJSON("GET", "/status/sessions", sessionsResp("abc", "playing", "42"))

	if !ConfirmEngaged(appleTV(), []string{"42"}) {
		t.Fatal("want engaged")
	}
	if *slept != 0 {
		t.Fatalf("slept %d times, want 0 (first probe is immediate)", *slept)
	}
}

// A session playing OTHER content on the client is not engagement — a stale
// session must never confirm a bind for a queue it isn't playing.
func TestConfirmEngagedRejectsOtherContent(t *testing.T) {
	f := newFakePMS(t)
	slept := fastVerify(t, 3)
	f.onJSON("GET", "/status/sessions", sessionsResp("abc", "playing", "999"))

	if ConfirmEngaged(appleTV(), []string{"42"}) {
		t.Fatal("want not engaged (different ratingKey)")
	}
	if *slept != 2 {
		t.Fatalf("slept %d times, want 2 (probes-1)", *slept)
	}
}

func TestConfirmEngagedIdleAndAbsentSessionsFail(t *testing.T) {
	f := newFakePMS(t)
	fastVerify(t, 2)
	f.onJSON("GET", "/status/sessions", sessionsResp("abc", "idle", "42"))
	if ConfirmEngaged(appleTV(), []string{"42"}) {
		t.Fatal("want not engaged (idle state)")
	}

	f.onJSON("GET", "/status/sessions", jsonx.J{"MediaContainer": jsonx.J{}})
	if ConfirmEngaged(appleTV(), []string{"42"}) {
		t.Fatal("want not engaged (no sessions)")
	}
}

// Unscoped mode (allowed empty — the queue's items couldn't be fetched):
// any non-idle session on the client counts.
func TestConfirmEngagedUnscopedAcceptsAnyNonIdle(t *testing.T) {
	f := newFakePMS(t)
	fastVerify(t, 2)
	f.onJSON("GET", "/status/sessions", sessionsResp("abc", "buffering", "whatever"))

	if !ConfirmEngaged(appleTV(), nil) {
		t.Fatal("want engaged")
	}
}

// Engagement can land on a later probe: probe 1 sees no session yet (the
// client hasn't reported to PMS), probe 2 confirms.
func TestConfirmEngagedConfirmsOnLaterProbe(t *testing.T) {
	f := newFakePMS(t)
	fastVerify(t, 3)
	f.onSequence("GET", "/status/sessions",
		jsonx.J{"MediaContainer": jsonx.J{}}, // probe 1: no session yet
		sessionsResp("abc", "playing", "42"), // probe 2: engaged
		sessionsResp("abc", "playing", "42"), // spare
	)

	if !ConfirmEngaged(appleTV(), []string{"42"}) {
		t.Fatal("want engaged on the second probe")
	}
}

// A sessions fetch failure must not abort (or crash) verification — the
// poll rides out the window and reports not-engaged.
func TestConfirmEngagedSessionsFetchFailureRidesOutWindow(t *testing.T) {
	f := newFakePMS(t)
	slept := fastVerify(t, 3)
	f.onStatus("GET", "/status/sessions", 500)

	if ConfirmEngaged(appleTV(), []string{"42"}) {
		t.Fatal("want not engaged")
	}
	if *slept != 2 {
		t.Fatalf("slept %d times, want 2 (kept polling through the failures)", *slept)
	}
}

// No machineIdentifier means unverifiable, not failed: report engaged and
// make no sessions calls at all.
func TestConfirmEngagedNoMachineIdentifierIsUnverifiable(t *testing.T) {
	f := newFakePMS(t)
	fastVerify(t, 2)

	if !ConfirmEngaged(jsonx.J{"name": "Apple TV"}, []string{"42"}) {
		t.Fatal("want true (unverifiable)")
	}
	if got := f.callCount("GET", "/status/sessions"); got != 0 {
		t.Fatalf("sessions calls = %d, want 0", got)
	}
}

func TestItemRatingKeys(t *testing.T) {
	f := newFakePMS(t)
	f.onJSON("GET", "/playQueues/9", jsonx.J{"MediaContainer": jsonx.J{"Metadata": []any{
		jsonx.J{"ratingKey": "1"},
		jsonx.J{"ratingKey": "2"},
		jsonx.J{"title": "no key"},
	}}})

	got := ItemRatingKeys("9")
	if !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("keys = %#v", got)
	}
	if ItemRatingKeys("404-me") != nil {
		t.Fatal("want nil on fetch failure")
	}
}
