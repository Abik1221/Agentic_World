package monopoly

import (
	"log/slog"
	"testing"
)

type stubMinter struct{}

func (stubMinter) Mint(agentID, matchID string, round int) string { return "tok" }

// The turn minter must reach the pushPlayer whichever ORDER the service is wired in.
//
// This is a regression test for a bug that produced no error, no log line and no failing test:
// main.go called SetTurnMinter AFTER EnablePushPlay, and EnablePushPlay copies s.turns by value,
// so the pusher held nil. Every Monopoly view then shipped with no turn proof, which means no
// Monopoly decision could ever be bound, no Monopoly agent could earn Verified, and the gateway
// would forward their calls forever without crediting one.
//
// Mafia had identical code wired in the opposite order and worked correctly. That is the worst
// shape a defect can take: the same code behaving differently because of a line number, with
// nothing in either package hinting that order mattered.
//
// Asserting the ORDER in main.go would be the fragile fix — the next setter added in the wrong
// place breaks it again. Asserting order-INDEPENDENCE is what actually holds.
func TestTurnMinterReachesThePusherInEitherWiringOrder(t *testing.T) {
	t.Run("minter before EnablePushPlay", func(t *testing.T) {
		s := &Service{}
		s.SetTurnMinter(stubMinter{})
		s.EnablePushPlay(nil, nil, slog.New(slog.DiscardHandler))
		if s.pusher.turns == nil {
			t.Fatal("pusher has no minter: views would ship with no turn proof")
		}
	})
	t.Run("minter after EnablePushPlay", func(t *testing.T) {
		s := &Service{}
		s.EnablePushPlay(nil, nil, slog.New(slog.DiscardHandler))
		s.SetTurnMinter(stubMinter{})
		if s.pusher.turns == nil {
			t.Fatal("pusher has no minter — this is the exact ordering main.go used, and it " +
				"silently disabled turn proofs for every Monopoly match")
		}
	})
}

// A nil minter must stay nil rather than becoming a forgeable placeholder: with
// TURN_PROOF_SECRET unset the signer is inert by construction, and a deployment that has not
// configured verification should ship views with NO proof rather than an empty one that looks
// like a proof and verifies against nothing.
func TestNoMinterMeansNoProofNotAnEmptyProof(t *testing.T) {
	s := &Service{}
	s.EnablePushPlay(nil, nil, slog.New(slog.DiscardHandler))
	if got := s.pusher.mintProof("ag", "m", 1); got != "" {
		t.Fatalf("mintProof with no minter returned %q, want empty", got)
	}
}
