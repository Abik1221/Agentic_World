package main

import (
	"encoding/json"
	"errors"
	"testing"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
	"github.com/agent-arena/arena/internal/monopoly"
)

// The lab agent must play complete Monopoly matches without ever proposing an illegal action.
//
// This is the test the lab did not have, and its absence is why the gap survived: /play answered
// Monopoly with `{}` and the platform's legal fallback moved the seat instead. On the wire a
// forfeit and a decision look the same, so the matches completed, the logs read normally, and
// nothing measured agent behaviour.
//
// Driving the real engine rather than a mock is the point. A mock would encode my belief about
// which verbs are legal in each phase, which is exactly the belief under test.
func TestLabAgentPlaysMonopolyToCompletion(t *testing.T) {
	for _, style := range []string{"balanced", "aggressive"} {
		t.Run(style, func(t *testing.T) {
			eng := mono.New(mono.Config{Players: 4, StartingCash: 1500, MaxTurns: 400})
			seed := []byte("gamelab-monopoly-" + style)
			st, _ := eng.Init(seed)

			p := persona{Style: style}
			var acted, rejected int

			for step := 0; step < 20000; step++ {
				if st.Finished {
					break
				}
				// Ask the ENGINE which seat it is waiting on. Deriving it here (st.Current, or
				// the auction's current bidder) looked right and was wrong in PhaseTrade, where
				// the actor comes off the trade queue — the same class of mistake as reading a
				// phase to guess whose turn it is.
				seat := eng.PendingSeat(st)
				if seat < 0 {
					t.Fatalf("step %d: engine reports no pending seat in phase %q", step, st.Phase)
				}
				legal := eng.LegalActions(st, seat)
				if len(legal) == 0 {
					t.Fatalf("step %d: engine offers seat %d no legal action in phase %q",
						step, seat, st.Phase)
				}

				act, why := monopolyAction(p, monopolyView{
					YourSeat: seat, Phase: st.Phase, Legal: legal, State: &st,
				})
				kind, _ := act["kind"].(string)
				if kind == "" {
					t.Fatalf("step %d: policy returned no kind (phase %q, legal %v)",
						step, st.Phase, legal)
				}
				// A bid must always carry a positive amount. This is the exact defect that used
				// to surface as "not legal in the current phase", and a lab agent reproducing it
				// would be teaching the wrong contract to anyone reading it as an example.
				if kind == mono.ActBid {
					amt, _ := act["amount"].(int)
					if amt <= 0 {
						t.Fatalf("step %d: bid with no positive amount (%v) — %s", step, act, why)
					}
				}

				// Carry EVERY field the policy set. The first version of this loop copied only
				// kind/property/amount and dropped the trade payload, so a correct propose_trade
				// reached the engine with a nil Trade and was rejected — the harness blaming the
				// policy for the harness's own omission.
				a := mono.Action{Kind: kind}
				if v, ok := act["property"].(int); ok {
					a.Property = v
				}
				if v, ok := act["amount"].(int); ok {
					a.Amount = v
				}
				if v, ok := act["trade"].(mono.Trade); ok {
					tr := v
					a.Trade = &tr
				}

				next, _, err := eng.Step(st, seat, a, seed)
				if err != nil {
					rejected++
					// Tolerating a couple of rejections would hide a policy that limps along on
					// the engine's forgiveness. Any rejection is a bug in the policy.
					t.Fatalf("step %d: engine rejected %q from seat %d in phase %q (legal %v): %v\nrationale: %s",
						step, kind, seat, st.Phase, legal, err, why)
				}
				st = next
				acted++
			}

			if !st.Finished {
				t.Fatalf("match did not finish in 20000 steps (phase %q after %d actions) — "+
					"a policy that stalls would hang a stress run rather than fail it",
					st.Phase, acted)
			}
			if acted < 50 {
				t.Fatalf("match finished after only %d actions; that is not a real game", acted)
			}
			if rejected != 0 {
				t.Fatalf("%d rejected actions", rejected)
			}
			t.Logf("%s: completed in %d agent actions", style, acted)
		})
	}
}

// The auction branch is the one the engine will punish hardest, and a random walk may not reach
// a deep auction. This drives it directly.
func TestLabAgentAuctionBidsAreAlwaysWellFormed(t *testing.T) {
	eng := mono.New(mono.Config{Players: 4, StartingCash: 1500, MaxTurns: 400})
	seed := []byte("gamelab-auction")
	st, _ := eng.Init(seed)
	st.Phase = mono.PhaseAuction
	st.Auction = &mono.AuctionState{
		Property: 1, HighBid: 0, HighBidder: mono.Bank,
		InAuction: []bool{true, true, true, true}, Current: 0,
	}

	p := persona{Style: "aggressive"}
	for i := 0; i < 400; i++ {
		if st.Auction == nil {
			return // auction closed cleanly, which is the success case
		}
		seat := st.Auction.Current
		legal := eng.LegalActions(st, seat)
		act, why := monopolyAction(p, monopolyView{
			YourSeat: seat, Phase: st.Phase, Legal: legal, State: &st,
		})
		kind, _ := act["kind"].(string)
		a := mono.Action{Kind: kind}
		if v, ok := act["amount"].(int); ok {
			a.Amount = v
		}
		if kind == mono.ActBid {
			if a.Amount <= 0 {
				t.Fatalf("iter %d: seat %d bid without an amount — %s", i, seat, why)
			}
			if a.Amount > st.Players[seat].Cash {
				t.Fatalf("iter %d: seat %d bid %d holding %d — the engine rejects this as "+
					"insufficient funds and the auction stalls", i, seat, a.Amount, st.Players[seat].Cash)
			}
		}
		next, _, err := eng.Step(st, seat, a, seed)
		if err != nil {
			if errors.Is(err, mono.ErrBidAmountMissing) {
				t.Fatalf("iter %d: the lab reproduced the amount-less bid defect: %v", i, err)
			}
			t.Fatalf("iter %d: engine rejected %q (amount %d) from seat %d: %v — %s",
				i, kind, a.Amount, seat, err, why)
		}
		st = next
	}
	t.Fatal("auction never closed in 400 iterations — every seat is raising forever")
}

// ── the wire contract ────────────────────────────────────────────────────────
//
// These marshal the PLATFORM'S OWN types and decode them with the lab's, rather than
// asserting against a hand-written JSON literal. A literal is written from the same
// misunderstanding that produced the bug, so it agrees with the bug; the platform's struct
// cannot.
//
// What they pin: the lab Monopoly agent read `your_seat` and `legal` while the platform has
// always sent `seat` and `legal_actions`, so every view decoded to zeros, the handler returned
// `{}` before logging anything, and the platform's safeFallback played BOTH seats of every lab
// Monopoly match. A full match ran to GAME END with zero agent decision lines. Nothing errored
// — an unplayed seat and a played one are the same bytes on the wire.

func TestMonopolyViewDecodesThePlatformsOwnPushView(t *testing.T) {
	st := &mono.State{
		Phase:     mono.PhaseManage,
		TurnCount: 7,
		Players:   []mono.Player{{Seat: 0, Cash: 1500}, {Seat: 1, Cash: 1200}},
	}
	sent := monopoly.MonopolyPushView{
		Game: "monopoly", MatchID: "mp_contract", Seat: 1,
		Phase: mono.PhaseManage, LegalActions: []string{mono.ActEndTurn, mono.ActBuild},
		State: st, Round: 7, TurnProof: "proof-abc",
	}
	raw, err := json.Marshal(sent)
	if err != nil {
		t.Fatal(err)
	}
	var got monopolyView
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Legal) != 2 {
		t.Fatalf("legal actions decoded as %v — an empty list makes the handler answer {} and "+
			"the platform play the seat for us, silently", got.Legal)
	}
	if got.YourSeat != 1 {
		t.Fatalf("seat decoded as %d, want 1 — YourSeat indexes State.Players, so a wrong seat "+
			"evaluates someone else's board", got.YourSeat)
	}
	if got.Round != 7 {
		t.Fatalf("round decoded as %d, want 7 — the turn proof is minted for this number", got.Round)
	}
	if got.TurnProof != "proof-abc" {
		t.Fatalf("turn proof decoded as %q; without it no Monopoly decision can be bound", got.TurnProof)
	}
	if got.MatchID != "mp_contract" || got.State == nil {
		t.Fatalf("match id or state lost: %+v", got)
	}
}

func TestMonopolyMoveDecodesIntoThePlatformsOwnPushMove(t *testing.T) {
	// What the lab actually puts on the wire, through the same translation the handler uses.
	wire := monopolyWireMove(map[string]any{
		"kind": mono.ActBuild, "property": 6, "rationale": "completing the set",
	})
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var got monopoly.MonopolyPushMove
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Action != mono.ActBuild {
		t.Fatalf("action decoded as %q — the lab sent `kind` (the engine's word) where the push "+
			"protocol reads `action`, so the platform saw an empty move, called it illegal and "+
			"substituted a fallback", got.Action)
	}
	if got.Property != 6 {
		t.Fatalf("property decoded as %d, want 6", got.Property)
	}
	if got.Rationale != "completing the set" {
		t.Fatalf("rationale decoded as %q", got.Rationale)
	}
}
