package ladder

import (
	"testing"

	"github.com/agent-arena/arena/internal/exploit"
	"github.com/agent-arena/arena/internal/gops"
)

func spec(t *testing.T) Spec {
	t.Helper()
	s := DefaultSpec()
	if err := s.Validate(); err != nil {
		t.Fatalf("DefaultSpec invalid: %v", err)
	}
	return s
}

// TestHashIsStableAndTotal. A certificate cites a spec version; if the hash for that version
// ever moves, every published certificate is orphaned and no audit can succeed.
func TestHashIsStableAndTotal(t *testing.T) {
	s := spec(t)
	h0, err := s.Hash()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		h, err := s.Hash()
		if err != nil {
			t.Fatal(err)
		}
		if h != h0 {
			t.Fatalf("hash is not stable: %s vs %s", h, h0)
		}
	}

	// Every field must be covered. A field outside the hash is a way to change what was
	// measured without changing what the certificate says was measured.
	mutate := map[string]func(*Spec){
		"N":                 func(x *Spec) { x.N = 4; x.PrizeOrders = [][]int{{1, 2, 3, 4}} },
		"PrizeOrders":       func(x *Spec) { x.PrizeOrders = [][]int{{5, 4, 3, 2, 1}} },
		"Alpha":             func(x *Spec) { x.Alpha = 0.25 },
		"Delta":             func(x *Spec) { x.Delta = 0.01 },
		"PhaseAMatches":     func(x *Spec) { x.PhaseAMatches = 61 },
		"FirstCheckpoint":   func(x *Spec) { x.FirstCheckpoint = 64 },
		"MaxPhaseB":         func(x *Spec) { x.MaxPhaseB = 4096 },
		"ProberMixture":     func(x *Spec) { x.ProberMixture = 3 },
		"TargetPrecision":   func(x *Spec) { x.TargetPrecision = 0.02 },
		"ReferenceOpponent": func(x *Spec) { x.ReferenceOpponent = RefNearestPool },
		"BudgetMS":          func(x *Spec) { x.BudgetMS = 700_000 },
		"PerMoveMS":         func(x *Spec) { x.PerMoveMS = 90_000 },
		"IncrementMS":       func(x *Spec) { x.IncrementMS = 1_000 },
		"GraceMS":           func(x *Spec) { x.GraceMS = 3_000 },
		"Version":           func(x *Spec) { x.Version = 2 },
	}
	for field, m := range mutate {
		t.Run(field, func(t *testing.T) {
			c := spec(t)
			m(&c)
			h, err := c.Hash()
			if err != nil {
				t.Fatalf("mutated spec invalid: %v", err)
			}
			if h == h0 {
				t.Fatalf("changing %s did not change the hash — it is outside the identity", field)
			}
		})
	}
}

// TestProberDigestIsOrderIndependent. Go map iteration is randomised; a digest that varied
// between runs over the same strategy would fail every audit for the wrong reason.
func TestProberDigestIsOrderIndependent(t *testing.T) {
	cfg, err := spec(t).Game()
	if err != nil {
		t.Fatal(err)
	}
	_, moves, err := gops.BestResponse(cfg, gops.Uniform())
	if err != nil {
		t.Fatal(err)
	}
	d0 := ProberDigest(cfg, moves)
	for i := 0; i < 50; i++ {
		// Rebuild the map so its internal ordering differs.
		copyM := make(map[gops.Node]int, len(moves))
		for k, v := range moves {
			copyM[k] = v
		}
		if d := ProberDigest(cfg, copyM); d != d0 {
			t.Fatalf("digest varies with map order: %s vs %s", d, d0)
		}
	}
}

// TestProberDigestDetectsAChangedStrategy — the point of pinning it.
func TestProberDigestDetectsAChangedStrategy(t *testing.T) {
	cfg, _ := spec(t).Game()
	_, moves, err := gops.BestResponse(cfg, gops.Uniform())
	if err != nil {
		t.Fatal(err)
	}
	d0 := ProberDigest(cfg, moves)
	for n := range moves {
		moves[n] = (moves[n] % cfg.N) + 1 // change exactly one node's action
		break
	}
	if ProberDigest(cfg, moves) == d0 {
		t.Fatal("digest did not change when the strategy did")
	}
}

// TestRunPlaysFitThenPinsThenCertifies walks the whole machine.
func TestRunPlaysFitThenPinsThenCertifies(t *testing.T) {
	s := spec(t)
	r := Run{ID: 1, AgentID: "ag", SpecVersion: s.Version, Phase: PhaseFit}

	a, err := Next(r, s, exploit.SequentialCertificate{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != ActionPlayFit || a.Matches != s.PhaseAMatches {
		t.Fatalf("first action %+v, want %d fit matches", a, s.PhaseAMatches)
	}

	r.FitDone = s.PhaseAMatches
	if a, err = Next(r, s, exploit.SequentialCertificate{}); err != nil {
		t.Fatal(err)
	} else if a.Kind != ActionComputeProber {
		t.Fatalf("action after a full fit sample = %+v, want compute_prober", a)
	}

	r, err = EnterCertify(r, "digest-abc")
	if err != nil {
		t.Fatal(err)
	}
	if r.Phase != PhaseCertify || r.ProberDigest != "digest-abc" {
		t.Fatalf("bad transition: %+v", r)
	}

	if a, err = Next(r, s, exploit.SequentialCertificate{}); err != nil {
		t.Fatal(err)
	}
	if a.Kind != ActionPlayCertify || a.Matches != s.FirstCheckpoint {
		t.Fatalf("action %+v, want a batch of %d to reach the first checkpoint", a, s.FirstCheckpoint)
	}
}

// TestPhaseBIsBatchedToCheckpoints. The bound may only be read at a checkpoint, so a smaller
// batch cannot produce a decision and only buys scheduling round-trips.
func TestPhaseBIsBatchedToCheckpoints(t *testing.T) {
	s := spec(t)
	r := Run{ID: 1, SpecVersion: s.Version, Phase: PhaseCertify, ProberDigest: "d"}
	for _, tc := range []struct{ done, want int }{
		{0, 32}, {32, 32}, {64, 64}, {100, 28}, {128, 128},
	} {
		r.CertifyDone = tc.done
		a, err := Next(r, s, exploit.SequentialCertificate{})
		if err != nil {
			t.Fatal(err)
		}
		if a.Matches != tc.want {
			t.Errorf("at %d played, batch %d, want %d", tc.done, a.Matches, tc.want)
		}
	}
}

// TestRunNeverBuysMatchesPastTheCostCeiling.
func TestRunNeverBuysMatchesPastTheCostCeiling(t *testing.T) {
	s := spec(t)
	s.MaxPhaseB = 100
	r := Run{ID: 1, SpecVersion: s.Version, Phase: PhaseCertify, ProberDigest: "d", CertifyDone: 90}
	a, err := Next(r, s, exploit.SequentialCertificate{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind == ActionPlayCertify && 90+a.Matches > s.MaxPhaseB {
		t.Fatalf("would play to %d, past the ceiling %d", 90+a.Matches, s.MaxPhaseB)
	}
	r.CertifyDone = 100
	if a, err = Next(r, s, exploit.SequentialCertificate{}); err != nil {
		t.Fatal(err)
	} else if a.Kind != ActionFinish || a.Reason != "budget_exhausted" {
		t.Fatalf("at the ceiling: %+v, want finish/budget_exhausted", a)
	}
}

// TestPreciseBoundFinishesEarly — the cost win, expressed in the state machine.
//
// The rule is PRECISION, not significance. An earlier version asserted "conclusive", i.e.
// finish as soon as the bound clears zero; that published the weakest bound the schedule
// could certify and was measured at Kendall tau 0.49 against the truth.
func TestPreciseBoundFinishesEarly(t *testing.T) {
	s := spec(t)
	r := Run{ID: 1, SpecVersion: s.Version, Phase: PhaseCertify, ProberDigest: "d", CertifyDone: 32}

	// Informative but IMPRECISE must keep playing.
	imprecise := exploit.SequentialCertificate{
		Ready: true, At: 32,
		Certificate: exploit.Certificate{Informative: true, MeanPayoff: 6.0, LowerBound: 1.2},
	}
	a, err := Next(r, s, imprecise)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind == ActionFinish {
		t.Fatalf("finished on a merely-informative bound: %+v", a)
	}

	// Precise finishes.
	precise := exploit.SequentialCertificate{
		Ready: true, At: 32,
		Certificate: exploit.Certificate{Informative: true, MeanPayoff: 3.0, LowerBound: 2.99},
	}
	if a, err = Next(r, s, precise); err != nil {
		t.Fatal(err)
	}
	if a.Kind != ActionFinish || a.Reason != "precision_reached" {
		t.Fatalf("%+v, want finish/precision_reached", a)
	}
}

// TestCertifyingWithoutAPinnedProberIsRefused. Measuring against a strategy nobody pinned
// produces a certificate that cannot be checked, and nothing downstream would notice.
func TestCertifyingWithoutAPinnedProberIsRefused(t *testing.T) {
	s := spec(t)
	r := Run{ID: 7, SpecVersion: s.Version, Phase: PhaseCertify}
	if _, err := Next(r, s, exploit.SequentialCertificate{}); err == nil {
		t.Fatal("certifying with no pinned prober was allowed")
	}
}

// TestAProberCannotBeRepinned. If it could, the strategy an agent was measured against could
// be swapped after the fact and the certificate would still look valid.
func TestAProberCannotBeRepinned(t *testing.T) {
	r := Run{ID: 1, Phase: PhaseFit}
	r, err := EnterCertify(r, "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnterCertify(r, "second"); err == nil {
		t.Fatal("a pinned prober was replaced")
	}
	if _, err := EnterCertify(Run{Phase: PhaseFit}, ""); err == nil {
		t.Fatal("an empty digest was accepted")
	}
}

// TestARewoundRunIsRefused. A run still fitting that already carries a digest has been
// rewound; continuing would fold Phase B play into the fit, which is exactly the leak sample
// splitting exists to prevent.
func TestARewoundRunIsRefused(t *testing.T) {
	s := spec(t)
	r := Run{ID: 3, SpecVersion: s.Version, Phase: PhaseFit, ProberDigest: "leaked"}
	if _, err := Next(r, s, exploit.SequentialCertificate{}); err == nil {
		t.Fatal("a rewound run was allowed to keep fitting")
	}
}

// TestARunCannotChangeSpecMidFlight. The prober, the deck and the clock would all move.
func TestARunCannotChangeSpecMidFlight(t *testing.T) {
	s := spec(t)
	r := Run{ID: 1, SpecVersion: 99, Phase: PhaseFit}
	if _, err := Next(r, s, exploit.SequentialCertificate{}); err == nil {
		t.Fatal("a run on another spec version was accepted")
	}
}

// TestFinishCarriesTheAuditTrail. A bound without its spec hash and prober digest names a
// measurement nobody can reconstruct.
func TestFinishCarriesTheAuditTrail(t *testing.T) {
	s := spec(t)
	r := Run{ID: 5, AgentID: "ag", SpecVersion: s.Version, Phase: PhaseCertify,
		ProberDigest: "pd", FitDone: 60, CertifyDone: 64}
	cert := exploit.SequentialCertificate{Ready: true, At: 64,
		Certificate: exploit.Certificate{Games: 64, LowerBound: 0.8, Informative: true}}

	out, after, err := Finish(r, s, cert, 4.5, true, "precision_reached", 0.97)
	if err != nil {
		t.Fatal(err)
	}
	if after.Phase != PhaseDone {
		t.Fatalf("phase after finish = %q", after.Phase)
	}
	wantHash, _ := s.Hash()
	if out.SpecHash != wantHash || out.ProberDigest != "pd" {
		t.Fatalf("certificate lacks its audit trail: %+v", out)
	}
	// Both halves of the interval must reach the certificate. A lower bound alone cannot
	// order agents, which is the whole reason the upper bound exists.
	if out.UpperBound != 4.5 || !out.Separable {
		t.Fatalf("the interval did not reach the certificate: %+v", out)
	}
	if out.FitMatches != 60 || out.KeptFraction != 0.97 || out.Games != 64 {
		t.Fatalf("certificate lost provenance: %+v", out)
	}
	if _, _, err := Finish(Run{Phase: PhaseFit}, s, cert, 4.5, true, "x", 1); err == nil {
		t.Fatal("finished a run that was still fitting")
	}
}

// TestClosedRunsDoNothing. Abandoned must never be read as "measured and unexploitable".
func TestClosedRunsDoNothing(t *testing.T) {
	s := spec(t)
	for _, p := range []Phase{PhaseDone, PhaseAbandoned} {
		a, err := Next(Run{SpecVersion: s.Version, Phase: p}, s, exploit.SequentialCertificate{})
		if err != nil {
			t.Fatal(err)
		}
		if a.Kind != ActionNone || a.Reason != string(p) {
			t.Fatalf("phase %q gave %+v", p, a)
		}
	}
}

func TestValidateRejectsUnusableSpecs(t *testing.T) {
	cases := map[string]func(*Spec){
		"bad version":         func(s *Spec) { s.Version = 0 },
		"bad delta":           func(s *Spec) { s.Delta = 0 },
		"negative alpha":      func(s *Spec) { s.Alpha = -1 },
		"no fit matches":      func(s *Spec) { s.PhaseAMatches = 0 },
		"checkpoint too low":  func(s *Spec) { s.FirstCheckpoint = 1 },
		"budget below a look": func(s *Spec) { s.MaxPhaseB = 8 },
		"bad tie rule":        func(s *Spec) { s.TieRule = "split" },
		"bad reference":       func(s *Spec) { s.ReferenceOpponent = "psychic" },
		"deck too large":      func(s *Spec) { s.N = 99 },
		"bad clock":           func(s *Spec) { s.PerMoveMS = 0 },
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			s := DefaultSpec()
			m(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("want error, got nil")
			}
			if _, err := s.Hash(); err == nil {
				t.Fatal("an invalid spec produced a hash")
			}
		})
	}
}
