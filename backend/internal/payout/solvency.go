package payout

import (
	"context"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// BalanceProvider reads an SPL-token account's balance in base units. Satisfied by
// the blockchain RPC client; kept as an interface so payout doesn't import it.
type BalanceProvider interface {
	TokenAccountBalance(ctx context.Context, tokenAccount string) (int64, error)
}

// liabilityRepo is the slice of the payout repo the monitor needs.
type liabilityRepo interface {
	OutstandingLiabilityCents(ctx context.Context) (int64, error)
}

// SolvencyMonitor periodically reconciles the platform hot wallet's ON-CHAIN USDC
// balance against the OUTSTANDING withdrawal liability (net cents the platform still
// owes on in-flight cash-outs). It is strictly READ-ONLY — it never moves funds; it
// emits metrics and a loud WARN if the treasury can't cover what's owed, catching
// treasury drain, fee/reconciliation drift, or a bug before it becomes an insolvency.
// USDC has 6 decimals, so 1 cent = 10^4 base units.
type SolvencyMonitor struct {
	repo     liabilityRepo
	balances BalanceProvider
	ata      string // platform hot-wallet USDC token account
	log      *slog.Logger
	balGauge prometheus.Gauge
	liaGauge prometheus.Gauge
	defGauge prometheus.Gauge // liability - balance, in cents (>0 ⇒ shortfall)
}

// NewSolvencyMonitor builds the monitor and registers its gauges.
func NewSolvencyMonitor(repo liabilityRepo, balances BalanceProvider, platformATA string, log *slog.Logger, reg *prometheus.Registry) *SolvencyMonitor {
	m := &SolvencyMonitor{
		repo: repo, balances: balances, ata: platformATA, log: log,
		balGauge: prometheus.NewGauge(prometheus.GaugeOpts{Name: "treasury_balance_cents", Help: "Hot-wallet USDC balance (cents)."}),
		liaGauge: prometheus.NewGauge(prometheus.GaugeOpts{Name: "withdrawal_liability_cents", Help: "Outstanding un-paid withdrawal liability (cents)."}),
		defGauge: prometheus.NewGauge(prometheus.GaugeOpts{Name: "treasury_shortfall_cents", Help: "Liability minus balance (cents); >0 means the treasury can't cover owed payouts."}),
	}
	reg.MustRegister(m.balGauge, m.liaGauge, m.defGauge)
	return m
}

// Check runs one reconciliation pass. Returns balance and liability in cents.
func (m *SolvencyMonitor) Check(ctx context.Context) (balanceCents, liabilityCents int64, err error) {
	base, err := m.balances.TokenAccountBalance(ctx, m.ata)
	if err != nil {
		return 0, 0, err
	}
	balanceCents = base / 10_000 // 6-decimal USDC base units → cents
	liabilityCents, err = m.repo.OutstandingLiabilityCents(ctx)
	if err != nil {
		return 0, 0, err
	}
	m.balGauge.Set(float64(balanceCents))
	m.liaGauge.Set(float64(liabilityCents))
	m.defGauge.Set(float64(liabilityCents - balanceCents))
	if balanceCents < liabilityCents {
		m.log.Error("payout: TREASURY SHORTFALL — hot-wallet USDC below outstanding withdrawal liability",
			"balance_cents", balanceCents, "liability_cents", liabilityCents,
			"shortfall_cents", liabilityCents-balanceCents, "ata", m.ata)
	}
	return balanceCents, liabilityCents, nil
}

// Run reconciles on a ticker until ctx is cancelled. A panic in a pass is recovered
// so the worker survives a malformed RPC response.
func (m *SolvencyMonitor) Run(every time.Duration) func(context.Context) {
	if every <= 0 {
		every = 5 * time.Minute
	}
	return func(ctx context.Context) {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				func() {
					defer func() {
						if r := recover(); r != nil {
							m.log.Error("payout solvency monitor panic", "recover", r)
						}
					}()
					if _, _, err := m.Check(ctx); err != nil {
						m.log.Warn("payout solvency monitor", "error", err)
					}
				}()
			}
		}
	}
}
