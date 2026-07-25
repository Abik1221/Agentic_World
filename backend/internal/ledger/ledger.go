package ledger

import (
	"context"
	"errors"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

// Service is the coin-mover. It enforces the balance invariant in code, then
// delegates the atomic write to the Repo. It is safe for concurrent use.
type Service struct {
	repo Repo
	m    *metrics
}

// New builds the ledger service and registers its collectors on reg.
func New(repo Repo, reg *prometheus.Registry) *Service {
	return &Service{repo: repo, m: newMetrics(reg)}
}

// Post validates that the transaction is non-empty and balanced (Σ == 0), then
// applies it atomically and idempotently. A replayed key returns Applied=false
// with no new effect.
func (s *Service) Post(ctx context.Context, t Txn) (ApplyResult, error) {
	if len(t.Postings) == 0 {
		return ApplyResult{}, ErrEmpty
	}
	var sum int64
	for _, p := range t.Postings {
		sum += p.Amount
	}
	if sum != 0 {
		s.m.unbalanced.Inc()
		return ApplyResult{}, ErrUnbalanced
	}

	res, err := s.repo.Apply(ctx, ApplyInput{
		PublicID: platform.NewID("txn"),
		Kind:     t.Kind,
		Key:      t.Key,
		Metadata: t.Metadata,
		Postings: t.Postings,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrInsufficient):
			s.m.negativeAttempts.Inc()
		case errors.Is(err, ErrInvariant):
			s.m.imbalance.Inc()
		}
		return ApplyResult{}, err
	}
	if res.Applied {
		s.m.posted.WithLabelValues(t.Kind).Inc()
	}
	return res, nil
}

// Balance returns an agent wallet's current balance.
func (s *Service) Balance(ctx context.Context, agentPublicID string) (int64, error) {
	return s.repo.Balance(ctx, agentPublicID)
}

// UserBalance returns an owner's treasury wallet balance.
func (s *Service) UserBalance(ctx context.Context, userPublicID string) (int64, error) {
	return s.repo.UserBalance(ctx, userPublicID)
}

// History returns an agent's ledger lines, newest first, skipping offset rows.
func (s *Service) History(ctx context.Context, agentPublicID string, limit, offset int) ([]Line, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.History(ctx, agentPublicID, limit, offset)
}

// UserHistory returns an owner's treasury ledger lines, newest first, skipping offset rows.
func (s *Service) UserHistory(ctx context.Context, userPublicID string, limit, offset int) ([]Line, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.UserHistory(ctx, userPublicID, limit, offset)
}

// ── metrics ──────────────────────────────────────────────────────────────────

type metrics struct {
	posted           *prometheus.CounterVec
	unbalanced       prometheus.Counter
	negativeAttempts prometheus.Counter
	imbalance        prometheus.Counter
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		posted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ledger_transactions_posted_total",
			Help: "Ledger transactions successfully applied, by kind.",
		}, []string{"kind"}),
		unbalanced: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ledger_unbalanced_rejected_total",
			Help: "Transactions rejected because their postings did not sum to zero (a bug).",
		}),
		negativeAttempts: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wallet_negative_attempts_total",
			Help: "Posts rejected because an agent wallet would go negative.",
		}),
		imbalance: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ledger_imbalance_detected_total",
			Help: "Protected-wallet invariant breaches or reconciliation drift (pages on any increment).",
		}),
	}
	reg.MustRegister(m.posted, m.unbalanced, m.negativeAttempts, m.imbalance)
	return m
}
