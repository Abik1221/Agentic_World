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
func (r *fakeRepo) RecordFlag(_ context.Context, agent, _, _, _ string) error {
	r.flagged[agent] = true
	return nil
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
	return nil, nil
}
func (r *fakeRepo) AgentsWithSamples(context.Context, int) ([]string, error) { return nil, nil }
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
