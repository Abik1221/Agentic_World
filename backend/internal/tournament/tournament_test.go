package tournament_test

import (
	"context"
	"testing"

	"github.com/agent-arena/arena/internal/tournament"
	"github.com/prometheus/client_golang/prometheus"
)

type fakeRepo struct {
	tourneys map[string]tournament.Tournament
	eligible map[string]bool
}

func newRepo() *fakeRepo {
	return &fakeRepo{tourneys: map[string]tournament.Tournament{}, eligible: map[string]bool{}}
}

func (r *fakeRepo) Create(_ context.Context, in tournament.CreateInput) (string, error) {
	r.tourneys[in.PublicID] = tournament.Tournament{
		PublicID: in.PublicID, Name: in.Name, Sponsor: in.Sponsor, PrizePool: in.PrizePool, Status: "open",
	}
	return in.PublicID, nil
}
func (r *fakeRepo) Get(_ context.Context, id string) (tournament.Tournament, error) {
	return r.tourneys[id], nil
}
func (r *fakeRepo) List(_ context.Context, _ int) ([]tournament.Tournament, error) {
	out := make([]tournament.Tournament, 0, len(r.tourneys))
	for _, t := range r.tourneys {
		out = append(out, t)
	}
	return out, nil
}
func (r *fakeRepo) Enter(_ context.Context, id, _ string) error {
	if r.tourneys[id].Status != "open" {
		return tournament.ErrClosed
	}
	return nil
}
func (r *fakeRepo) Eligible(_ context.Context, agent string) (bool, string, error) {
	if r.eligible[agent] {
		return true, "", nil
	}
	return false, "not tournament_ready", nil
}
func (r *fakeRepo) Finalize(_ context.Context, id, winner string) (tournament.Tournament, bool, error) {
	t := r.tourneys[id]
	firstTime := t.Status != "finished"
	if firstTime {
		t.Status, t.Winner = "finished", winner
		r.tourneys[id] = t
	}
	return t, firstTime, nil
}

type payment struct {
	agent string
	coins int64
}

// fakeBank models the ledger's per-tournament idempotency keys.
type fakeBank struct {
	funded   map[string]int64
	paid     map[string]payment
	payCalls int
}

func newBank() *fakeBank {
	return &fakeBank{funded: map[string]int64{}, paid: map[string]payment{}}
}
func (b *fakeBank) FundPool(_ context.Context, id string, coins int64) error {
	b.funded[id] = coins
	return nil
}
func (b *fakeBank) PayWinner(_ context.Context, id, agent string, coins int64) error {
	if _, ok := b.paid[id]; ok {
		return nil // idem key payout:tourney:{id} → single effect
	}
	b.paid[id] = payment{agent, coins}
	b.payCalls++
	return nil
}

func newSvc(repo tournament.Repo, bank tournament.Bank) *tournament.Service {
	return tournament.New(repo, bank, prometheus.NewRegistry())
}

func TestCreateFundsPool(t *testing.T) {
	repo, bank := newRepo(), newBank()
	id, err := newSvc(repo, bank).Create(context.Background(), "Hero Cup", "Acme", 1000)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if bank.funded[id] != 1000 {
		t.Fatalf("pool funded = %d, want 1000", bank.funded[id])
	}
}

func TestEnterRequiresEligibility(t *testing.T) {
	repo, bank := newRepo(), newBank()
	svc := newSvc(repo, bank)
	ctx := context.Background()
	id, _ := svc.Create(ctx, "Hero Cup", "Acme", 1000)

	if err := svc.Enter(ctx, "ag_bad", id); err != tournament.ErrIneligible {
		t.Fatalf("ineligible entry = %v, want ErrIneligible", err)
	}
	repo.eligible["ag_ok"] = true
	if err := svc.Enter(ctx, "ag_ok", id); err != nil {
		t.Fatalf("eligible entry: %v", err)
	}
}

func TestFinalizePaysChampionOnceIdempotent(t *testing.T) {
	repo, bank := newRepo(), newBank()
	svc := newSvc(repo, bank)
	ctx := context.Background()
	id, _ := svc.Create(ctx, "Hero Cup", "Acme", 1000)
	repo.eligible["ag_w"] = true

	if err := svc.Finalize(ctx, id, "ag_w"); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	// Re-finalize must not pay twice.
	if err := svc.Finalize(ctx, id, "ag_w"); err != nil {
		t.Fatalf("re-finalize: %v", err)
	}
	if bank.payCalls != 1 {
		t.Fatalf("payouts = %d, want exactly 1", bank.payCalls)
	}
	if bank.paid[id] != (payment{"ag_w", 1000}) {
		t.Fatalf("paid = %+v, want {ag_w 1000}", bank.paid[id])
	}
}

func TestFinalizeRejectsIneligibleWinner(t *testing.T) {
	repo, bank := newRepo(), newBank()
	svc := newSvc(repo, bank)
	ctx := context.Background()
	id, _ := svc.Create(ctx, "Hero Cup", "Acme", 1000)

	if err := svc.Finalize(ctx, id, "ag_flagged"); err != tournament.ErrIneligible {
		t.Fatalf("ineligible winner = %v, want ErrIneligible", err)
	}
	if bank.payCalls != 0 {
		t.Fatal("a flagged winner must not be paid")
	}
}
