package tournament

import (
	"context"
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	ErrIneligible = httpx.NewError(http.StatusForbidden, "ineligible", "Agent is not eligible for funded play.")
	ErrClosed     = httpx.NewError(http.StatusConflict, "tournament_closed", "This tournament is not open for entry.")
	ErrBadPool    = httpx.NewError(http.StatusBadRequest, "invalid_pool", "Prize pool must be positive.")
)

// Service runs the freeroll lifecycle: fund on create, eligibility-gated entry,
// and a champion payout through the validation gate at finalize.
type Service struct {
	repo Repo
	bank Bank
	m    *metrics
}

func New(repo Repo, bank Bank, reg *prometheus.Registry) *Service {
	return &Service{repo: repo, bank: bank, m: newMetrics(reg)}
}

// Create opens a tournament and funds its pool (sponsor → escrow, idempotent).
func (s *Service) Create(ctx context.Context, name, sponsor string, pool int64) (string, error) {
	if pool <= 0 {
		return "", ErrBadPool
	}
	id, err := s.repo.Create(ctx, CreateInput{PublicID: platform.NewID("trn"), Name: name, Sponsor: sponsor, PrizePool: pool})
	if err != nil {
		return "", err
	}
	if err := s.bank.FundPool(ctx, id, pool); err != nil {
		return "", err
	}
	s.m.created.Inc()
	return id, nil
}

// Enter records a free entry for an eligible agent.
func (s *Service) Enter(ctx context.Context, agentPublicID, tournamentPublicID string) error {
	ok, _, err := s.repo.Eligible(ctx, agentPublicID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrIneligible
	}
	if err := s.repo.Enter(ctx, tournamentPublicID, agentPublicID); err != nil {
		return err
	}
	s.m.entries.Inc()
	return nil
}

// Get returns the public tournament view.
func (s *Service) Get(ctx context.Context, tournamentPublicID string) (Tournament, error) {
	return s.repo.Get(ctx, tournamentPublicID)
}

// List returns tournaments for discovery (open/upcoming first).
func (s *Service) List(ctx context.Context, limit int) ([]Tournament, error) {
	return s.repo.List(ctx, limit)
}

// Finalize names the champion (first call) and pays the pool, idempotently. The
// winner must be eligible (no fraud hold) before the pool is decided — money is
// never paid to a flagged agent.
func (s *Service) Finalize(ctx context.Context, tournamentPublicID, winnerAgentPublicID string) error {
	cur, err := s.repo.Get(ctx, tournamentPublicID)
	if err != nil {
		return err
	}
	winner := winnerAgentPublicID
	if cur.Status == "finished" {
		winner = cur.Winner // idempotent: keep the recorded champion
	} else {
		ok, _, err := s.repo.Eligible(ctx, winner)
		if err != nil {
			return err
		}
		if !ok {
			return ErrIneligible
		}
	}

	t, _, err := s.repo.Finalize(ctx, tournamentPublicID, winner)
	if err != nil {
		return err
	}
	if t.Winner == "" {
		return nil // no champion to pay (defensive)
	}
	if err := s.bank.PayWinner(ctx, tournamentPublicID, t.Winner, t.PrizePool); err != nil {
		return err
	}
	s.m.paid.Inc()
	return nil
}

// ── metrics ──────────────────────────────────────────────────────────────────

type metrics struct {
	created prometheus.Counter
	entries prometheus.Counter
	paid    prometheus.Counter
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		created: prometheus.NewCounter(prometheus.CounterOpts{Name: "tournaments_created_total", Help: "Tournaments created."}),
		entries: prometheus.NewCounter(prometheus.CounterOpts{Name: "tournament_entries_total", Help: "Free entries recorded."}),
		paid:    prometheus.NewCounter(prometheus.CounterOpts{Name: "tournament_payouts_total", Help: "Tournament champion payouts."}),
	}
	reg.MustRegister(m.created, m.entries, m.paid)
	return m
}
