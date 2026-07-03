package monopoly

import (
	"encoding/json"
	"math/rand"
	"testing"
)

// ── A random-but-legal driving policy, used to fuzz full games ──────────────

type policy struct{ rng *rand.Rand }

func (p policy) act(e *Engine, s State) (int, Action) {
	actor := e.pendingActor(s)
	switch s.Phase {
	case PhaseRoll:
		return actor, Action{Kind: ActRoll}
	case PhaseJail:
		return actor, Action{Kind: ActRollJail}
	case PhaseAcquire:
		pos := s.Players[actor].Position
		if s.Players[actor].Cash >= space(pos).Price && p.rng.Intn(100) < 85 {
			return actor, Action{Kind: ActBuy}
		}
		return actor, Action{Kind: ActDecline}
	case PhaseAuction:
		au := s.Auction
		next := au.HighBid + 1
		if next <= s.Players[actor].Cash && au.HighBid < space(au.Property).Price && p.rng.Intn(100) < 40 {
			return actor, Action{Kind: ActBid, Amount: next}
		}
		return actor, Action{Kind: ActPass}
	case PhaseResolveDebt:
		if pos, ok := firstSellable(&s, actor); ok {
			return actor, Action{Kind: ActSellHouse, Property: pos}
		}
		if pos, ok := firstMortgageable(&s, actor); ok {
			return actor, Action{Kind: ActMortgage, Property: pos}
		}
		return actor, Action{Kind: ActBankrupt}
	case PhaseManage:
		if p.rng.Intn(100) < 35 {
			if pos, ok := firstBuildable(&s, actor); ok {
				return actor, Action{Kind: ActBuild, Property: pos}
			}
		}
		return actor, Action{Kind: ActEndTurn}
	}
	return actor, Action{Kind: ActEndTurn}
}

// (The firstMortgageable / firstSellable / firstBuildable legality helpers now
// live in legal.go so the bots and table runner can share them.)

// driveGame plays a full match with the given policy and returns the final state
// plus the complete event log.
func driveGame(t *testing.T, e *Engine, seed []byte, p policy) (State, []Event) {
	t.Helper()
	s, evs := e.Init(seed)
	for i := 0; i < 500000 && !s.Finished; i++ {
		seat, a := p.act(e, s)
		ns, ev, err := e.Step(s, seat, a, seed)
		if err != nil {
			t.Fatalf("step %d: phase=%s seat=%d action=%+v err=%v", i, s.Phase, seat, a, err)
		}
		s = ns
		evs = append(evs, ev...)
		checkInvariants(t, s, i)
	}
	if !s.Finished {
		t.Fatal("game did not terminate within the step budget")
	}
	// The whole log must be a gap-free 0..n-1 sequence (checked once, not O(n^2)).
	for i, ev := range evs {
		if ev.Seq != i {
			t.Fatalf("event %d has seq %d (log not gap-free)", i, ev.Seq)
		}
	}
	return s, evs
}

// checkInvariants asserts the structural rules that must hold after every step.
func checkInvariants(t *testing.T, s State, step int) {
	t.Helper()

	// Phase/pointer consistency: a debt/auction phase must have its data.
	if s.Phase == PhaseResolveDebt && s.Debt == nil {
		t.Fatalf("step %d: phase resolve_debt with nil Debt", step)
	}
	if s.Phase == PhaseAuction && s.Auction == nil {
		t.Fatalf("step %d: phase auction with nil Auction", step)
	}

	housesOnBoard, hotelsOnBoard := 0, 0
	for idx := 0; idx < BoardSize; idx++ {
		h := s.Holdings[idx]
		if h.Owner == Bank {
			if h.Houses != 0 || h.Mortgaged {
				t.Fatalf("step %d: unowned square %d has buildings/mortgage", step, idx)
			}
			continue
		}
		if h.Owner < 0 || h.Owner >= len(s.Players) {
			t.Fatalf("step %d: square %d has invalid owner %d", step, idx, h.Owner)
		}
		if s.Players[h.Owner].Bankrupt {
			t.Fatalf("step %d: bankrupt player %d still owns square %d", step, h.Owner, idx)
		}
		if h.Houses == 5 {
			hotelsOnBoard++
		} else {
			housesOnBoard += h.Houses
		}
	}

	// Bank building supply is conserved.
	if housesOnBoard+s.HousesRemaining != 32 {
		t.Fatalf("step %d: houses %d + remaining %d != 32", step, housesOnBoard, s.HousesRemaining)
	}
	if hotelsOnBoard+s.HotelsRemaining != 12 {
		t.Fatalf("step %d: hotels %d + remaining %d != 12", step, hotelsOnBoard, s.HotelsRemaining)
	}

	for i, pl := range s.Players {
		if pl.Cash < 0 {
			t.Fatalf("step %d: player %d has negative cash %d", step, i, pl.Cash)
		}
		if pl.Position < 0 || pl.Position >= BoardSize {
			t.Fatalf("step %d: player %d off-board at %d", step, i, pl.Position)
		}
		if pl.Bankrupt && (pl.Cash != 0) {
			t.Fatalf("step %d: bankrupt player %d holds cash %d", step, i, pl.Cash)
		}
	}
}

func TestPropertyInvariantsManyGames(t *testing.T) {
	for trial := 0; trial < 200; trial++ {
		e := New(Config{Players: 2 + trial%3, StartingCash: 1500, MaxTurns: 250})
		p := policy{rng: rand.New(rand.NewSource(int64(trial)))}
		seed := []byte{byte(trial), byte(trial >> 8), 0x5a}
		s, evs := driveGame(t, e, seed, p)
		if s.Winner != Tie && (s.Winner < 0 || s.Winner >= len(s.Players)) {
			t.Fatalf("trial %d: invalid winner %d", trial, s.Winner)
		}
		if evs[len(evs)-1].Type != EvMatchFinished {
			t.Fatalf("trial %d: last event is %s, want match_finished", trial, evs[len(evs)-1].Type)
		}
	}
}

// TestDeterminismSameSeedSamePolicy proves that identical seed + identical policy
// reproduce the match exactly — the basis of replay and provable fairness.
func TestDeterminismSameSeedSamePolicy(t *testing.T) {
	for trial := 0; trial < 30; trial++ {
		seed := []byte{0x11, byte(trial), 0x22}
		run := func() (State, []Event) {
			e := New(Config{Players: 3, StartingCash: 1500, MaxTurns: 250})
			p := policy{rng: rand.New(rand.NewSource(int64(trial)))}
			return driveGame(t, e, seed, p)
		}
		s1, e1 := run()
		s2, e2 := run()

		if mustJSON(t, s1) != mustJSON(t, s2) {
			t.Fatalf("trial %d: final state differs between identical runs", trial)
		}
		if len(e1) != len(e2) {
			t.Fatalf("trial %d: event counts differ: %d vs %d", trial, len(e1), len(e2))
		}
		for i := range e1 {
			if mustJSON(t, e1[i]) != mustJSON(t, e2[i]) {
				t.Fatalf("trial %d: event %d differs", trial, i)
			}
		}
	}
}

// TestForceTimeoutDeterministic drives a whole match purely through ForceTimeout
// (no policy randomness) and confirms two runs are byte-identical.
func TestForceTimeoutDeterministic(t *testing.T) {
	seed := []byte("timeout-determinism")
	run := func() (State, []Event) {
		e := New(Config{Players: 4, StartingCash: 1500, MaxTurns: 120})
		s, evs := e.Init(seed)
		for i := 0; i < 100000 && !s.Finished; i++ {
			ns, ev, err := e.ForceTimeout(s, seed)
			if err != nil {
				t.Fatalf("force timeout: %v", err)
			}
			s = ns
			evs = append(evs, ev...)
		}
		if !s.Finished {
			t.Fatal("timeout-driven game did not finish")
		}
		return s, evs
	}
	s1, e1 := run()
	s2, e2 := run()
	if mustJSON(t, s1) != mustJSON(t, s2) || len(e1) != len(e2) {
		t.Fatal("ForceTimeout is not deterministic")
	}
}

// TestPrizeDecksDeterministic checks the seed-shuffled decks are reproducible and
// are valid permutations.
func TestDecksDeterministic(t *testing.T) {
	seed := []byte("deck-seed")
	a := derivedDeckOrder(seed, "chance", len(chanceDeck))
	b := derivedDeckOrder(seed, "chance", len(chanceDeck))
	if mustJSON(t, a) != mustJSON(t, b) {
		t.Fatal("deck order not deterministic for the same seed")
	}
	seen := make([]bool, len(chanceDeck))
	for _, id := range a {
		if id < 0 || id >= len(chanceDeck) || seen[id] {
			t.Fatalf("deck order is not a permutation: %v", a)
		}
		seen[id] = true
	}
}

// TestReplayReproducesMatch proves a recorded match replays to the identical state
// and event log, that the replay hash matches, and that tampering is detected.
func TestReplayReproducesMatch(t *testing.T) {
	for trial := 0; trial < 30; trial++ {
		cfg := Config{Players: 2 + trial%3, StartingCash: 1500, MaxTurns: 250}
		seed := []byte{byte(trial), 0x9c, byte(trial >> 8)}
		tbl := NewTable(cfg, seed, nil)
		tbl.PlayOut()

		s2, log2, err := Replay(cfg, seed, tbl.Moves())
		if err != nil {
			t.Fatalf("trial %d: replay error: %v", trial, err)
		}
		if mustJSON(t, tbl.State()) != mustJSON(t, s2) {
			t.Fatalf("trial %d: replayed final state differs", trial)
		}
		if len(tbl.Log()) != len(log2) || tbl.ReplayHash() != ReplayHash(log2) {
			t.Fatalf("trial %d: replayed log/hash differs", trial)
		}
		if ok, err := Verify(cfg, seed, tbl.Moves(), tbl.Log()); err != nil || !ok {
			t.Fatalf("trial %d: Verify failed (ok=%v err=%v)", trial, ok, err)
		}
		// Tampering with the event log must fail verification.
		bad := append([]Event(nil), tbl.Log()...)
		if len(bad) > 3 {
			bad[3].Type = "tampered"
			if ok, _ := Verify(cfg, seed, tbl.Moves(), bad); ok {
				t.Fatalf("trial %d: Verify accepted a tampered log", trial)
			}
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
