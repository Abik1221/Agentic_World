package store

import (
	"testing"

	"github.com/agent-arena/arena/internal/platformsign"
)

// These constants mirror internal/platformsign KAT and the Super Admin's
// configbus KAT. Together they prove the engine signs events in exactly the
// byte-format the Admin verifies.
const (
	katSeedB64 = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA="
	katSigB64  = "mpEEop+Aw2mYnYMJue69h5Ql8AwRHXvR8KDeOk4KXNcOGacZf+QzajK+6wEpJeTilL8NCqr5EIttLm0pgXKdAQ=="
)

func TestEventSigningInputFormat(t *testing.T) {
	got := string(eventSigningInput("evt_kat_1", "match.finished", `{"a":1}`, "1720099200000"))
	want := "evt_kat_1\nmatch.finished\n{\"a\":1}\n1720099200000"
	if got != want {
		t.Fatalf("signing input format drifted:\n got  %q\n want %q", got, want)
	}
}

func TestEventSignatureMatchesKAT(t *testing.T) {
	s, err := platformsign.NewSigner(katSeedB64)
	if err != nil {
		t.Fatal(err)
	}
	sig := s.Sign(eventSigningInput("evt_kat_1", "match.finished", `{"a":1}`, "1720099200000"))
	if sig != katSigB64 {
		t.Fatalf("engine event signature drifted from KAT:\n got  %s\n want %s", sig, katSigB64)
	}
}
