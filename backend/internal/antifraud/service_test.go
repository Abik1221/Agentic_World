package antifraud_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/antifraud"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

type fakeRepo struct {
	agents       map[string][]antifraud.AgentRef
	flagged      map[string]bool
	holds        map[string]string
	disputeMatch map[string]string
	resolved     map[string]bool
	pairs        []antifraud.Pair
	votes        []antifraud.VotePair
	trades       []antifraud.Trade
}

func newRepo() *fakeRepo {
	return &fakeRepo{
		agents: map[string][]antifraud.AgentRef{}, flagged: map[string]bool{},
		holds: map[string]string{}, disputeMatch: map[string]string{}, resolved: map[string]bool{},
	}
}

func (r *fakeRepo) MatchAgents(_ context.Context, m string) ([]antifraud.AgentRef, error) {
	return r.agents[m], nil
}
func (r *fakeRepo) AnyFlagged(_ context.Context, ids []string) (bool, error) {
	for _, id := range ids {
		if r.flagged[id] {
			return true, nil
		}
	}
	return false, nil
}
func (r *fakeRepo) RecordFlag(_ context.Context, agent, _, _, _ string) (bool, error) {
	created := !r.flagged[agent]
	r.flagged[agent] = true
	return created, nil
}
func (r *fakeRepo) RecordHold(_ context.Context, m, reason string) (bool, error) {
	if _, ok := r.holds[m]; ok {
		return false, nil
	}
	r.holds[m] = reason
	return true, nil
}
func (r *fakeRepo) ResolveHold(_ context.Context, m, status string) (bool, error) {
	// Mirror the real atomic claim: only a still-open hold can be transitioned once.
	cur, ok := r.holds[m]
	if !ok || cur == "refunded" || cur == "released" {
		return false, nil
	}
	r.holds[m] = status
	return true, nil
}
func (r *fakeRepo) OpenDispute(_ context.Context, in antifraud.DisputeInput) (string, error) {
	return in.PublicID, nil
}
func (r *fakeRepo) ResolveDispute(_ context.Context, id, _, _ string) (string, bool, error) {
	if r.resolved[id] {
		return "", false, nil
	}
	r.resolved[id] = true
	return r.disputeMatch[id], true, nil
}
func (r *fakeRepo) RecentPairs(context.Context, time.Time, int) ([]antifraud.Pair, error) {
	return r.pairs, nil
}
func (r *fakeRepo) AgentsWithSamples(context.Context, int) ([]string, error) { return nil, nil }

// votes and trades let a test drive the multi-seat sweep. Nil by default, so every existing
// test keeps its old behaviour: an empty evidence set can never trip a detector, which is the
// right default for a fake that most tests do not care about.
func (r *fakeRepo) PairVotes(context.Context, string, string, time.Time) ([]antifraud.VotePair, error) {
	return r.votes, nil
}

func (r *fakeRepo) PairTrades(context.Context, string, string, time.Time) ([]antifraud.Trade, error) {
	return r.trades, nil
}

func (r *fakeRepo) PairMoves(context.Context, string, string, time.Time) ([]antifraud.MoveSample, error) {
	return nil, nil
}
func (r *fakeRepo) AgentTiming(context.Context, string) (antifraud.TimingStat, error) {
	return antifraud.TimingStat{}, nil
}
func (r *fakeRepo) Audit(context.Context, string, string, string, []byte) error { return nil }

type fakeSettler struct{ refunds, releases int }

func (s *fakeSettler) SettleHeld(context.Context, string) error { s.releases++; return nil }
func (s *fakeSettler) Refund(context.Context, string) error     { s.refunds++; return nil }

func newSvc(repo antifraud.Repo, settler antifraud.Settler) *antifraud.Service {
	return antifraud.New(repo, settler, platform.FixedClock{T: time.Unix(1_700_000_000, 0).UTC()},
		antifraud.Config{}, slog.Default(), prometheus.NewRegistry())
}

func TestGateHoldsSameOwner(t *testing.T) {
	repo := newRepo()
	repo.agents["m1"] = []antifraud.AgentRef{{AgentPublicID: "ag_a", OwnerPublicID: "usr_x"}, {AgentPublicID: "ag_b", OwnerPublicID: "usr_x"}}
	allowed, err := newSvc(repo, &fakeSettler{}).Allow(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("same-owner match must be held, not paid out")
	}
	if repo.holds["m1"] == "" {
		t.Fatal("expected a hold recorded for the same-owner match")
	}
}

func TestGateAllowsCleanMatch(t *testing.T) {
	repo := newRepo()
	repo.agents["m1"] = []antifraud.AgentRef{{AgentPublicID: "ag_a", OwnerPublicID: "usr_a"}, {AgentPublicID: "ag_b", OwnerPublicID: "usr_b"}}
	allowed, _ := newSvc(repo, &fakeSettler{}).Allow(context.Background(), "m1")
	if !allowed {
		t.Fatal("a clean match should pay out")
	}
}

func TestGateHoldsFlaggedAgent(t *testing.T) {
	repo := newRepo()
	repo.agents["m1"] = []antifraud.AgentRef{{AgentPublicID: "ag_a", OwnerPublicID: "usr_a"}, {AgentPublicID: "ag_b", OwnerPublicID: "usr_b"}}
	repo.flagged["ag_b"] = true
	allowed, _ := newSvc(repo, &fakeSettler{}).Allow(context.Background(), "m1")
	if allowed {
		t.Fatal("a match with a flagged agent must be held")
	}
}

func TestResolveDisputeRefundIdempotent(t *testing.T) {
	repo := newRepo()
	repo.disputeMatch["d1"] = "m1"
	repo.holds["m1"] = "held" // the match is under an open payout hold (refundable)
	settler := &fakeSettler{}
	svc := newSvc(repo, settler)
	ctx := context.Background()

	if err := svc.ResolveDispute(ctx, "usr_admin", "d1", "refund"); err != nil {
		t.Fatal(err)
	}
	// Second resolve is a no-op (already terminal).
	if err := svc.ResolveDispute(ctx, "usr_admin", "d1", "refund"); err != nil {
		t.Fatal(err)
	}
	if settler.refunds != 1 {
		t.Fatalf("refund applied %d times, want exactly 1 (idempotent)", settler.refunds)
	}
	if repo.holds["m1"] != "refunded" {
		t.Fatalf("hold status = %q, want refunded", repo.holds["m1"])
	}
}

// TestResolveDisputeRefundRequiresOpenHold is the H2 regression: a dispute-refund
// on a match that has NO open hold (e.g. one already cleanly settled, or already
// refunded/released) must NOT disburse — otherwise it debits the shared escrow a
// second time. Two distinct disputes on the same match must disburse at most once.
func TestResolveDisputeRefundRequiresOpenHold(t *testing.T) {
	ctx := context.Background()

	// (a) No hold at all (match settled cleanly) → refund must be a no-op.
	repo := newRepo()
	repo.disputeMatch["d1"] = "m_settled"
	settler := &fakeSettler{}
	if err := newSvc(repo, settler).ResolveDispute(ctx, "usr_admin", "d1", "refund"); err != nil {
		t.Fatal(err)
	}
	if settler.refunds != 0 {
		t.Fatalf("refund applied %d times on an unheld (settled) match, want 0", settler.refunds)
	}

	// (b) One held match, two different disputes → release then refund; only the
	// first (release) may disburse. The second must not double-pay.
	repo2 := newRepo()
	repo2.disputeMatch["dRel"] = "m2"
	repo2.disputeMatch["dRef"] = "m2"
	repo2.holds["m2"] = "held"
	settler2 := &fakeSettler{}
	svc2 := newSvc(repo2, settler2)
	if err := svc2.ResolveDispute(ctx, "usr_admin", "dRel", "release"); err != nil {
		t.Fatal(err)
	}
	if err := svc2.ResolveDispute(ctx, "usr_admin", "dRef", "refund"); err != nil {
		t.Fatal(err)
	}
	if settler2.releases != 1 || settler2.refunds != 0 {
		t.Fatalf("release+refund race: releases=%d refunds=%d, want exactly one disbursement (releases=1 refunds=0)", settler2.releases, settler2.refunds)
	}
}

type fakeClawback struct{ debt map[string]int64 }

func (c *fakeClawback) RecordFraudDebt(_ context.Context, agent string, coins int64, _ string) error {
	if c.debt == nil {
		c.debt = map[string]int64{}
	}
	c.debt[agent] += coins
	return nil
}

// M4: a new collusion flag claws back the net flow (flagged-match winnings) from the
// RECIPIENT once; a second detection sweep must not stack the debt.
func TestCollusionClawbackOncePerFlag(t *testing.T) {
	repo := newRepo()
	// A won 6-0 over B and 900 coins net flowed A->B ... net flow is A->B positive, so
	// B is the recipient of the dumped coins.
	repo.pairs = []antifraud.Pair{{A: "ag_a", B: "ag_b", Games: 6, AWins: 0, BWins: 6, NetFlowAToB: 900}}
	cb := &fakeClawback{}
	svc := newSvc(repo, &fakeSettler{})
	svc.SetClawback(cb)
	ctx := context.Background()

	if err := svc.RunDetection(ctx); err != nil {
		t.Fatal(err)
	}
	if cb.debt["ag_b"] != 900 {
		t.Fatalf("recipient debt = %d, want 900", cb.debt["ag_b"])
	}
	if cb.debt["ag_a"] != 0 {
		t.Fatalf("net loser was charged: %d, want 0", cb.debt["ag_a"])
	}
	// Second sweep: flags already exist (created=false) → no additional clawback.
	if err := svc.RunDetection(ctx); err != nil {
		t.Fatal(err)
	}
	if cb.debt["ag_b"] != 900 {
		t.Fatalf("debt stacked on re-sweep: %d, want 900", cb.debt["ag_b"])
	}
}

// TestSweepFlagsMafiaVoteCollusion drives the multi-seat sweep end to end through the
// service: a pair in the suspicious band whose votes are perfectly coordinated must be
// flagged for review.
func TestSweepFlagsMafiaVoteCollusion(t *testing.T) {
	repo := newRepo()
	// A pair concentrated enough to enter the suspicious band but under the ban threshold.
	repo.pairs = []antifraud.Pair{{A: "ag_r1", B: "ag_r2", Games: 40, AWins: 30, BWins: 10, NetFlowAToB: 500}}
	for i := 0; i < 40; i++ {
		repo.votes = append(repo.votes, antifraud.VotePair{TargetA: i % 7, TargetB: i % 7})
	}
	if err := newSvc(repo, &fakeSettler{}).RunDetection(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !repo.flagged["ag_r1"] || !repo.flagged["ag_r2"] {
		t.Fatal("a perfectly coordinated Mafia pair was not flagged by the sweep")
	}
}

// TestSweepFlagsMonopolyGifting — same path, the trade signal.
func TestSweepFlagsMonopolyGifting(t *testing.T) {
	repo := newRepo()
	repo.pairs = []antifraud.Pair{{A: "ag_gift", B: "ag_take", Games: 40, AWins: 30, BWins: 10, NetFlowAToB: 500}}
	for i := 0; i < 20; i++ {
		repo.trades = append(repo.trades, antifraud.Trade{GaveA: 600, GaveB: 5})
	}
	if err := newSvc(repo, &fakeSettler{}).RunDetection(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !repo.flagged["ag_gift"] || !repo.flagged["ag_take"] {
		t.Fatal("systematic Monopoly gifting was not flagged by the sweep")
	}
}

// TestSweepLeavesHonestMultiSeatPlayAlone is the one that protects the beta.
//
// A pair in the suspicious band whose votes merely CONVERGE — the game working — and whose
// trades are balanced must not be flagged. A detector that holds honest developers' money is
// worse than no detector, because it teaches them the platform is unsafe.
func TestSweepLeavesHonestMultiSeatPlayAlone(t *testing.T) {
	repo := newRepo()
	repo.pairs = []antifraud.Pair{{A: "ag_h1", B: "ag_h2", Games: 40, AWins: 30, BWins: 10, NetFlowAToB: 500}}
	for i := 0; i < 60; i++ {
		a := i % 9
		b := a
		if i%3 == 0 {
			b = (a + 4) % 9 // they follow consensus most rounds, independently
		}
		repo.votes = append(repo.votes, antifraud.VotePair{TargetA: a, TargetB: b})
	}
	for i := 0; i < 30; i++ {
		repo.trades = append(repo.trades, antifraud.Trade{GaveA: 200, GaveB: 180})
	}
	if err := newSvc(repo, &fakeSettler{}).RunDetection(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.flagged["ag_h1"] || repo.flagged["ag_h2"] {
		t.Fatal("honest convergent play and balanced trading were flagged; this would hold " +
			"honest developers' money")
	}
}
