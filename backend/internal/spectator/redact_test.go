package spectator

import (
	"testing"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
)

// The generic /v1/match/{id}/watch stream serves EVERY match id, not just
// Goofspiel — and it is public and unauthenticated. Mafia writes its night
// actions to the same event log, so before spectatorSafe existed, streaming a
// LIVE mafia match here returned things like:
//
//	{"actor":"Mafia","seat":5,"secret":"Target → seat 8"}
//
// i.e. every role and every night target, in real time, to anyone who asked.
// `pyyol watch <match>` uses this endpoint, so it was one command away.
//
// These tests pin both halves of the guard. They are cheap and they protect the
// platform's stated rule that hidden information never leaves the server.

func TestNightEventsNeverReachLiveSpectators(t *testing.T) {
	ev := gs.Event{
		Seq:  2,
		Type: "night",
		Payload: map[string]any{
			"actor":  "Mafia",
			"seat":   5,
			"secret": "Target → seat 8",
			"text":   "Mafia member 5 selects a target.",
		},
	}
	if spectatorSafe(ev) {
		t.Fatal("a mafia night event was judged safe for a live spectator — " +
			"this is the role/target leak on /v1/match/{id}/watch")
	}
}

// The kind check alone is not enough: a game that adds a new secret-bearing
// event under a different type would leak through a type allowlist nobody
// remembered to update. The payload rule is what makes that safe by default.
func TestAnyPayloadCarryingASecretIsDropped(t *testing.T) {
	ev := gs.Event{
		Seq:     7,
		Type:    "some_future_kind",
		Payload: map[string]any{"seat": 3, "secret": "the detective checked seat 9"},
	}
	if spectatorSafe(ev) {
		t.Fatal("an event carrying a `secret` field was judged safe for a live spectator")
	}
}

// The public events are the whole point of the stream — redaction that ate them
// would leave a spectator watching nothing, so pin that they still pass.
func TestPublicEventsStillStream(t *testing.T) {
	for _, ev := range []gs.Event{
		{Seq: 1, Type: "phase", Payload: map[string]any{"day": 1, "phase": "night"}},
		{Seq: 3, Type: "moderator", Payload: map[string]any{"text": "Dawn breaks."}},
		{Seq: 4, Type: "message", Payload: map[string]any{"from": 2, "text": "seat 5 is quiet"}},
		{Seq: 5, Type: "vote", Payload: map[string]any{"from": 2, "target": 5}},
		{Seq: 6, Type: "eliminate", Payload: map[string]any{"target": 5, "cause": "vote"}},
	} {
		if !spectatorSafe(ev) {
			t.Fatalf("public %q event was withheld from spectators", ev.Type)
		}
	}
}

// A payload that is not an object (Goofspiel sends typed structs, and some
// events carry scalars) has no fields to leak and must not be dropped.
func TestNonObjectPayloadsAreNotDropped(t *testing.T) {
	if !spectatorSafe(gs.Event{Seq: 1, Type: "round_revealed", Payload: nil}) {
		t.Fatal("a nil payload was dropped")
	}
	if !spectatorSafe(gs.Event{Seq: 2, Type: "tick", Payload: 42}) {
		t.Fatal("a scalar payload was dropped")
	}
}
