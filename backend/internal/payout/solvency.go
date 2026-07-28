package payout

import (
	"context"
	"log/slog"
	"sync"
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

	// last is the most recent successful reading, so the admin dashboard can show
	// the real on-chain treasury without every viewer triggering an RPC call. Kept
	// behind a mutex because Run() writes it and HTTP handlers read it.
	mu   sync.RWMutex
	last Reading
}

// Reading is one solvency observation. ObservedAt is zero until the first successful
// check, which callers MUST treat as "unknown" rather than as a zero balance — the
// difference between "we hold nothing" and "we have not looked yet" is the difference
// between an incident and a cold start.
type Reading struct {
	BalanceCents   int64
	LiabilityCents int64
	ObservedAt     time.Time
}

// LastReading returns the most recent successful reconciliation. ok is false before
// the first one lands, and callers must render that as "unknown" rather than zero.
// The flat signature satisfies adminapi.TreasuryReader without that package having
// to import payout.
func (m *SolvencyMonitor) LastReading() (balanceCents, liabilityCents int64, observedAt time.Time, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.last.BalanceCents, m.last.LiabilityCents, m.last.ObservedAt, !m.last.ObservedAt.IsZero()
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
	m.mu.Lock()
	m.last = Reading{BalanceCents: balanceCents, LiabilityCents: liabilityCents, ObservedAt: time.Now().UTC()}
	m.mu.Unlock()
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
