package mafia

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRedactLogRemovesNightSecrets proves the spectator-facing log carries no
// hidden information: night events (and their targets/findings/secrets) are
// dropped, while every public event is preserved in order and by sequence.
func TestRedactLogRemovesNightSecrets(t *testing.T) {
	tbl := NewTable(StandardSeats(), []byte("redact-seed"), nil, 60)
	tbl.PlayOut()
	full := tbl.Log()

	// Sanity: the full server-side log must actually contain night secrets,
	// otherwise this test would pass vacuously.
	var nights int
	for _, ev := range full {
		if ev.Type == EvNight {
			nights++
		}
	}
	if nights == 0 {
		t.Fatal("expected the full log to contain night events to redact")
	}

	pub := RedactLog(full)

	for _, ev := range pub {
		if ev.Type == EvNight {
			t.Fatalf("night event leaked into redacted log at seq %d", ev.Seq)
		}
	}

	// The serialized redacted log must not contain any hidden marker. "MAFIA"/
	// "TOWN" (all caps) only ever appear as a detective Finding; "Target →" and
	// the "secret"/"finding" JSON keys only exist on NightPayload.
	blob, _ := json.Marshal(pub)
	for _, needle := range []string{"\"secret\"", "\"finding\"", "Target →", "MAFIA", "TOWN"} {
		if strings.Contains(string(blob), needle) {
			t.Fatalf("redacted log leaked hidden marker %q", needle)
		}
	}

	// Every public event survives, in the same order and sequence.
	var wantPublic []Event
	for _, ev := range full {
		if ev.Type != EvNight {
			wantPublic = append(wantPublic, ev)
		}
	}
	if len(pub) != len(wantPublic) {
		t.Fatalf("redacted length %d, want %d public events", len(pub), len(wantPublic))
	}
	for i := range pub {
		if pub[i].Seq != wantPublic[i].Seq || pub[i].Type != wantPublic[i].Type {
			t.Fatalf("public event %d mismatch: got seq=%d type=%s", i, pub[i].Seq, pub[i].Type)
		}
	}
}

// TestPublicEvent enumerates the redaction predicate for every event kind.
func TestPublicEvent(t *testing.T) {
	public := []EventType{EvPhase, EvModerator, EvMessage, EvVote, EvEliminate, EvVictory}
	for _, ty := range public {
		if !PublicEvent(Event{Type: ty}) {
			t.Fatalf("%s should be public", ty)
		}
	}
	if PublicEvent(Event{Type: EvNight}) {
		t.Fatal("night events must never be public")
	}
}
