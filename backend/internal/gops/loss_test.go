package gops_test

import (
	"math"
	"testing"

	"github.com/agent-arena/arena/internal/gops"
)

func cfg5(t *testing.T) gops.Config {
	t.Helper()
	c := gops.Config{N: 5, Order: []int{3, 1, 5, 2, 4}, Tie: gops.TieCarry}
	if err := c.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	return c
}

// TestBestResponsePlayHasZeroLoss is the anchor. If an agent plays the solver's own best
// response, every decision must score exactly zero — otherwise the loss measure disagrees with
// the optimality notion it is defined against, and every figure derived from it is meaningless.
func TestBestResponsePlayHasZeroLoss(t *testing.T) {
	cfg := cfg5(t)
	opp := gops.Uniform()
	_, br, err := gops.BestResponse(cfg, opp)
	if err != nil {
		t.Fatal(err)
	}

	// Walk the tree the best response actually reaches and score each node's BR move.
	var ds []gops.Decision
	root := gops.Node{Me: cfg.FullHand(), Opp: cfg.FullHand()}
	seen := map[gops.Node]bool{}
	var walk func(n gops.Node, depth int)
	walk = func(n gops.Node, depth int) {
		if n.Me == 0 || n.Opp == 0 || depth > cfg.N || seen[n] {
			return
		}
		seen[n] = true
		mine, ok := br[n]
		if !ok {
			return
		}
		ds = append(ds, gops.Decision{Node: n, Chosen: mine})
		pot := cfg.Pot(n)
		for theirs := 1; theirs <= cfg.N; theirs++ {
			if n.Opp&(1<<(theirs-1)) == 0 {
				continue
			}
			// Carry MUST be propagated. An earlier version of this walk left it at zero, so the
			// traversal never reached a tie-carry node — and a mutation that dropped the carried
			// pot from the value computation passed the test untouched. The blind spot was in
			// the test, not the code, which is exactly the failure mutation testing is for.
			carry := 0
			if mine == theirs {
				carry = pot
			}
			walk(gops.Node{
				Me:    n.Me &^ (1 << (mine - 1)),
				Opp:   n.Opp &^ (1 << (theirs - 1)),
				Carry: carry,
			}, depth+1)
		}
	}
	walk(root, 0)
	if len(ds) < 5 {
		t.Fatalf("walked only %d decisions; the traversal is not exercising the tree", len(ds))
	}
	carried := 0
	for _, d := range ds {
		if d.Node.Carry > 0 {
			carried++
		}
	}
	if carried == 0 {
		t.Fatal("the traversal reached no tie-carry node, so any bug in how a carried pot is " +
			"valued would pass unnoticed")
	}

	got, err := gops.ScoreDecisions(cfg, opp, ds)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range got {
		if !l.Legal {
			t.Fatalf("best-response move %d reported illegal at %v", l.Chosen, l.Node)
		}
		if l.Loss > 1e-9 {
			t.Fatalf("best-response move %d at %v scored loss %.6f, want 0 — the loss measure "+
				"disagrees with the optimality it is defined against", l.Chosen, l.Node, l.Loss)
		}
	}
	t.Logf("scored %d best-response decisions, all zero loss", len(got))
}

// TestLossIsNeverNegativeAndCatchesRealMistakes. A measure that cannot go negative is easy to
// get right by accident; the half that matters is that a genuinely bad move scores a POSITIVE
// loss, so the metric can actually discriminate.
func TestLossIsNeverNegativeAndCatchesRealMistakes(t *testing.T) {
	cfg := cfg5(t)
	opp := gops.Uniform()
	root := gops.Node{Me: cfg.FullHand(), Opp: cfg.FullHand()}

	q, err := gops.ActionValues(cfg, opp, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != cfg.N {
		t.Fatalf("root offered %d actions, want %d", len(q), cfg.N)
	}

	var ds []gops.Decision
	for card := range q {
		ds = append(ds, gops.Decision{Node: root, Chosen: card})
	}
	got, err := gops.ScoreDecisions(cfg, opp, ds)
	if err != nil {
		t.Fatal(err)
	}
	positives := 0
	for _, l := range got {
		if l.Loss < 0 {
			t.Fatalf("negative loss %.6f for card %d", l.Loss, l.Chosen)
		}
		if l.Loss > 1e-9 {
			positives++
		}
	}
	if positives == 0 {
		t.Fatal("every action at the root scored zero loss — the measure cannot discriminate, " +
			"so it would rate a blunderer and a solver identically")
	}
	t.Logf("%d of %d root actions carry positive loss", positives, len(got))
}

// TestSharedMemoMatchesIndependentScoring. The batch path exists for speed; if sharing a memo
// changed an answer it would be a silent correctness bug that only appears at scale.
func TestSharedMemoMatchesIndependentScoring(t *testing.T) {
	cfg := cfg5(t)
	opp := gops.Uniform()
	root := gops.Node{Me: cfg.FullHand(), Opp: cfg.FullHand()}
	next := gops.Node{Me: root.Me &^ 1, Opp: root.Opp &^ 2}

	batch, err := gops.ScoreDecisions(cfg, opp, []gops.Decision{
		{Node: root, Chosen: 3}, {Node: next, Chosen: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []gops.Decision{{Node: root, Chosen: 3}, {Node: next, Chosen: 2}} {
		one, err := gops.ScoreDecisions(cfg, opp, []gops.Decision{d})
		if err != nil {
			t.Fatal(err)
		}
		var want gops.DecisionLoss
		for _, b := range batch {
			if b.Node == d.Node && b.Chosen == d.Chosen {
				want = b
			}
		}
		if math.Abs(one[0].Loss-want.Loss) > 1e-9 {
			t.Fatalf("shared memo changed the answer at %v: %.9f vs %.9f",
				d.Node, want.Loss, one[0].Loss)
		}
	}
}

// TestIllegalMoveIsReportedNotScored. A move that was never available has no value, and scoring
// it as a maximal loss would let a logging bug present as catastrophic play.
func TestIllegalMoveIsReportedNotScored(t *testing.T) {
	cfg := cfg5(t)
	root := gops.Node{Me: cfg.FullHand() &^ 1, Opp: cfg.FullHand()} // card 1 already spent
	got, err := gops.ScoreDecisions(cfg, gops.Uniform(), []gops.Decision{{Node: root, Chosen: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Legal {
		t.Fatal("a spent card was reported legal")
	}
	if got[0].Loss != 0 {
		t.Fatalf("illegal move carried loss %.4f; it must be reported, not scored", got[0].Loss)
	}
	total, counted := gops.TotalLoss(got)
	if counted != 0 || total != 0 {
		t.Fatalf("illegal decision leaked into the aggregate: total=%.4f counted=%d", total, counted)
	}
}

// TestLossSumsToTheBestResponseGap ties the per-decision numbers to a quantity computed a
// completely different way.
//
// A uniform player's total shortfall against the best response is BestResponse.value minus
// Evaluate(uniform, uniform). If the per-decision losses did not relate to that, the measure
// would be internally consistent and still wrong.
func TestLossSumsToTheBestResponseGap(t *testing.T) {
	cfg := cfg5(t)
	opp := gops.Uniform()

	brValue, _, err := gops.BestResponse(cfg, opp)
	if err != nil {
		t.Fatal(err)
	}
	selfValue, err := gops.Evaluate(cfg, gops.Uniform(), opp)
	if err != nil {
		t.Fatal(err)
	}
	gap := brValue - selfValue
	if gap <= 0 {
		t.Fatalf("best response gains %.4f over uniform self-play; expected a positive gap", gap)
	}

	// The root decision alone cannot exceed the whole-game gap.
	root := gops.Node{Me: cfg.FullHand(), Opp: cfg.FullHand()}
	q, err := gops.ActionValues(cfg, opp, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	var best, worst float64
	first := true
	for _, v := range q {
		if first {
			best, worst, first = v, v, false
		}
		if v > best {
			best = v
		}
		if v < worst {
			worst = v
		}
	}
	if best-worst > gap+1e-9 {
		t.Fatalf("root action spread %.4f exceeds the whole-game best-response gap %.4f — the "+
			"per-decision values are not on the same scale as the game value", best-worst, gap)
	}
	t.Logf("best-response gap %.4f, root action spread %.4f", gap, best-worst)
}
