package payout

import (
	"context"
	"fmt"
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

// FeeProvider reads an address's native SOL balance in lamports. Separate from
// BalanceProvider because it answers a different question: not "can we cover what we
// owe" but "can we broadcast at all".
type FeeProvider interface {
	LamportBalance(ctx context.Context, address string) (int64, error)
}

// liabilityRepo is the slice of the payout repo the monitor needs.
type liabilityRepo interface {
	OutstandingLiabilityCents(ctx context.Context) (int64, error)
}

// lamportsPerSOL renders lamports as SOL in log lines. Every threshold and metric
// stays in lamports so nothing depends on float rounding.
const lamportsPerSOL = 1_000_000_000

// SolvencyMonitor periodically reconciles the platform's ON-CHAIN USDC against the
// OUTSTANDING withdrawal liability (net cents still owed on in-flight cash-outs). It
// is strictly READ-ONLY — it never moves funds; it emits metrics and loud logs when
// the treasury can't cover what's owed, catching treasury drain, fee/reconciliation
// drift, or a bug before it becomes an insolvency. USDC has 6 decimals, so 1 cent =
// 10^4 base units.
//
// It also answers, synchronously and without I/O, whether a specific payout can be
// settled right now — see CanPay. That is what lets withdrawal approval refuse before
// broadcasting instead of after.
type SolvencyMonitor struct {
	repo     liabilityRepo
	balances BalanceProvider
	ata      string // hot-wallet USDC token account: the account payouts are signed FROM
	log      *slog.Logger
	balGauge prometheus.Gauge
	liaGauge prometheus.Gauge
	defGauge prometheus.Gauge // liability - custody, in cents (>0 ⇒ shortfall)
	expGauge prometheus.Gauge // hot balance above the exposure ceiling (>0 ⇒ sweep)
	vltGauge prometheus.Gauge // vault (deposit) USDC balance, in cents
	cusGauge prometheus.Gauge // hot + vault, in cents: everything the platform holds
	solGauge prometheus.Gauge // hot-wallet native SOL, in lamports (fee fuel)

	// exposureCapCents is the most we are willing to leave sitting in the hot wallet.
	// The hot wallet holds a live signing key, so its balance IS the maximum a key
	// compromise can take. Float only the working capital that outstanding payouts
	// actually need and sweep the rest to a cold address the server cannot sign for.
	// Zero disables the check (the correct default for devnet, where the tokens are
	// worthless and an alert would only be noise).
	exposureCapCents int64

	// vaultATA is the deposit destination when custody is SPLIT — deposits land in an
	// account this process holds no key for, and payouts are signed from a separately
	// funded float. Empty when both are the same account (the single-wallet setup), in
	// which case there is no second balance to read.
	//
	// The split is what makes the exposure cap mean anything. With one account,
	// deposits keep arriving into the signing wallet and the cap is breached by
	// ordinary business rather than by mismanagement — which teaches the operator to
	// ignore the one alert that limits blast radius.
	vaultATA string

	// coldAddress is named in the sweep alert. A "sweep the excess" warning that does
	// not say where to sweep it to is an instruction the operator has to go and look up
	// under time pressure, which is when the wrong address gets pasted.
	coldAddress string

	// hotWallet is the signer's own pubkey (not its token account) — native SOL lives
	// on the wallet, and that SOL pays every payout's transaction fee.
	hotWallet string
	fees      FeeProvider
	// minFeeLamports is the floor below which the payout rail is about to stop. Zero
	// disables the check.
	minFeeLamports int64

	// staleAfter is how old a reading may be before CanPay stops trusting it. Derived
	// from the run interval rather than configured: the useful definition of stale is
	// "we should have looked again by now", which only the cadence knows.
	staleAfter time.Duration

	// last is the most recent successful reading, so the admin dashboard can show the
	// real on-chain treasury without every viewer triggering an RPC call. Kept behind a
	// mutex because Run() writes it and HTTP handlers read it.
	mu   sync.RWMutex
	last Reading
}

// Reading is one solvency observation. ObservedAt is zero until the first successful
// check, which callers MUST treat as "unknown" rather than as a zero balance — the
// difference between "we hold nothing" and "we have not looked yet" is the difference
// between an incident and a cold start.
type Reading struct {
	// CustodyCents is everything the platform holds on-chain: hot + vault. This is the
	// figure that answers "are we solvent", because a user's claim is on the platform,
	// not on one particular account of it.
	CustodyCents int64
	// HotCents is the subset of custody that can be paid out right now without a human
	// moving funds. Below liability while custody covers it, the problem is a top-up,
	// not an insolvency — two very different pages at 3am.
	HotCents   int64
	VaultCents int64
	// VaultKnown is false when custody is split and the vault balance could not be
	// read, so CustodyCents is hot-only and understates what we hold.
	VaultKnown     bool
	LiabilityCents int64
	// FeeLamports is the hot wallet's native SOL; FeeKnown separates "0 lamports" (out
	// of gas) from "not checked", which are opposite conclusions.
	FeeLamports int64
	FeeKnown    bool
	ObservedAt  time.Time
}

// LastReading returns the most recent successful reconciliation. ok is false before
// the first one lands, and callers must render that as "unknown" rather than zero. The
// flat signature satisfies adminapi.TreasuryReader without that package having to
// import payout.
//
// It reports CUSTODY rather than the hot balance: under split custody the hot wallet
// is deliberately a small float, and showing that as "the treasury" would render a
// solvent platform as one holding almost nothing.
func (m *SolvencyMonitor) LastReading() (balanceCents, liabilityCents int64, observedAt time.Time, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.last.CustodyCents, m.last.LiabilityCents, m.last.ObservedAt, !m.last.ObservedAt.IsZero()
}

// The three accessors below back the admin treasury panel. They are deliberately flat
// (no payout types in the signatures) so adminapi can consume them through a local
// interface without importing this package — the same reason LastReading is shaped the
// way it is.

// CustodyBreakdown reports where the platform's USDC is sitting. split is true when
// deposits and payouts use different accounts. ok is false before the first observation,
// which a consumer must render as "unknown" rather than as zeroes.
func (m *SolvencyMonitor) CustodyBreakdown() (hotCents, vaultCents int64, split, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.last.HotCents, m.last.VaultCents, m.vaultATA != "", !m.last.ObservedAt.IsZero()
}

// FeeFuel reports the hot wallet's native SOL and the configured floor, in lamports.
// known is false when the check is not configured or the last read failed — which is
// not the same as a zero balance and must not be shown as one.
func (m *SolvencyMonitor) FeeFuel() (lamports, minLamports int64, known bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.last.FeeLamports, m.minFeeLamports, m.last.FeeKnown
}

// SweepNeeded reports how far the hot wallet sits above its exposure ceiling, the
// ceiling itself, and where a sweep should go. excessCents is 0 when within the cap or
// when no cap is set.
func (m *SolvencyMonitor) SweepNeeded() (excessCents, capCents int64, coldAddress string) {
	m.mu.RLock()
	hot, observed := m.last.HotCents, m.last.ObservedAt
	m.mu.RUnlock()
	if m.exposureCapCents <= 0 || observed.IsZero() {
		return 0, m.exposureCapCents, m.coldAddress
	}
	if excess := hot - m.exposureCapCents; excess > 0 {
		return excess, m.exposureCapCents, m.coldAddress
	}
	return 0, m.exposureCapCents, m.coldAddress
}

// NewSolvencyMonitor builds the monitor and registers its gauges. hotATA is the token
// account payouts are signed from.
func NewSolvencyMonitor(repo liabilityRepo, balances BalanceProvider, hotATA string, log *slog.Logger, reg *prometheus.Registry) *SolvencyMonitor {
	m := &SolvencyMonitor{
		repo: repo, balances: balances, ata: hotATA, log: log,
		balGauge: prometheus.NewGauge(prometheus.GaugeOpts{Name: "treasury_balance_cents", Help: "Hot-wallet USDC balance (cents) — the account payouts are signed from."}),
		liaGauge: prometheus.NewGauge(prometheus.GaugeOpts{Name: "withdrawal_liability_cents", Help: "Outstanding un-paid withdrawal liability (cents)."}),
		defGauge: prometheus.NewGauge(prometheus.GaugeOpts{Name: "treasury_shortfall_cents", Help: "Liability minus total custody (cents); >0 means the platform can't cover owed payouts."}),
		expGauge: prometheus.NewGauge(prometheus.GaugeOpts{Name: "treasury_excess_exposure_cents", Help: "Hot-wallet balance above the exposure ceiling (cents); >0 means sweep to cold storage."}),
		vltGauge: prometheus.NewGauge(prometheus.GaugeOpts{Name: "treasury_vault_balance_cents", Help: "Deposit-vault USDC balance (cents); 0 when custody is not split."}),
		cusGauge: prometheus.NewGauge(prometheus.GaugeOpts{Name: "treasury_custody_cents", Help: "Total on-chain USDC held (cents): hot wallet + deposit vault."}),
		solGauge: prometheus.NewGauge(prometheus.GaugeOpts{Name: "hot_wallet_sol_lamports", Help: "Hot-wallet native SOL (lamports); pays every payout's transaction fee."}),
	}
	reg.MustRegister(m.balGauge, m.liaGauge, m.defGauge, m.expGauge, m.vltGauge, m.cusGauge, m.solGauge)
	return m
}

// SetExposureCap sets the hot-wallet exposure ceiling in cents. Zero disables the
// check. This monitor deliberately only ALERTS: sweeping funds out would need a second
// signing key held by this same process, which would recreate on the sweep path
// exactly the exposure the cap exists to limit. The sweep is a human action against a
// cold address, and this is the signal to perform it.
func (m *SolvencyMonitor) SetExposureCap(cents int64) { m.exposureCapCents = cents }

// SetColdAddress records where the operator should sweep excess float to, so the alert
// can name it. Advisory only — nothing here ever signs a transfer.
func (m *SolvencyMonitor) SetColdAddress(addr string) { m.coldAddress = addr }

// SetVault points the monitor at the deposit account when custody is split. Passing
// the hot ATA (or an empty string) leaves the monitor in single-wallet mode, where
// custody is simply the hot balance and no second RPC call is made.
func (m *SolvencyMonitor) SetVault(vaultATA string) {
	if vaultATA == m.ata {
		vaultATA = ""
	}
	m.vaultATA = vaultATA
}

// SetFeeWatch enables the native-SOL check on the signing wallet. hotWallet is the
// signer's pubkey — not its token account, which holds USDC and no SOL. minLamports of
// zero disables the check, which is the right default on devnet where SOL is airdropped.
//
// This is the failure with no other symptom: USDC solvency can be perfect, the breaker
// green, every gate open, and every cash-out still fails at broadcast because the
// wallet cannot pay a fee. Nothing else in this system looks at SOL.
func (m *SolvencyMonitor) SetFeeWatch(fees FeeProvider, hotWallet string, minLamports int64) {
	m.fees, m.hotWallet, m.minFeeLamports = fees, hotWallet, minLamports
}

// CanPay reports whether a payout of amountCents can be settled from the hot wallet
// right now, and if not, a sentence an operator can act on.
//
// Reads the cached reading and performs NO I/O: approval is an interactive admin
// action, and making it wait on an RPC round trip would trade a real latency cost for
// a marginally fresher number.
//
// FAILS OPEN on anything it does not actually know — no reading yet (cold start), or a
// reading too old to trust. A monitor that blocks payouts because its own RPC is down
// converts an observability outage into a money outage, and the broadcast that follows
// would report the true problem anyway. It refuses only what it can currently prove.
func (m *SolvencyMonitor) CanPay(amountCents int64) (ok bool, reason string) {
	m.mu.RLock()
	r, stale := m.last, m.staleAfter
	m.mu.RUnlock()

	if r.ObservedAt.IsZero() {
		return true, ""
	}
	if stale > 0 && time.Since(r.ObservedAt) > stale {
		return true, ""
	}
	// SOL first: without fees nothing can be broadcast regardless of the USDC balance,
	// and "top up SOL" is a different action from "top up USDC". Reporting the wrong
	// one sends the operator to move the wrong asset.
	if m.minFeeLamports > 0 && r.FeeKnown && r.FeeLamports < m.minFeeLamports {
		return false, fmt.Sprintf(
			"the payout wallet is low on SOL (%.4f SOL, floor %.4f) and cannot pay transaction fees — fund %s with SOL, then approve",
			float64(r.FeeLamports)/lamportsPerSOL, float64(m.minFeeLamports)/lamportsPerSOL, m.hotWallet)
	}
	if r.HotCents < amountCents {
		return false, fmt.Sprintf(
			"the payout wallet holds $%.2f and this cash-out needs $%.2f — top up the float from cold storage, then approve",
			float64(r.HotCents)/100, float64(amountCents)/100)
	}
	return true, ""
}

// Check runs one reconciliation pass. Returns the hot-wallet balance and the liability
// in cents (the hot balance, not custody, because that is the existing contract).
//
// A failure to read the vault or the SOL balance does NOT fail the pass. The hot
// balance vs liability comparison is the check that matters most, and losing it because
// a secondary RPC call timed out would silence the monitor exactly when the network is
// flaky.
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

	// Vault (split custody only). Read second and non-fatally: a vault we cannot read
	// makes the solvency picture incomplete, not wrong, and understating custody errs
	// toward alarm rather than toward silence.
	var vaultCents int64
	vaultKnown := m.vaultATA == ""
	if m.vaultATA != "" {
		vb, verr := m.balances.TokenAccountBalance(ctx, m.vaultATA)
		if verr != nil {
			m.log.Warn("payout: deposit vault balance unreadable — custody figure is hot-wallet only",
				"vault_ata", m.vaultATA, "error", verr)
		} else {
			vaultCents, vaultKnown = vb/10_000, true
		}
	}
	custodyCents := balanceCents + vaultCents

	m.balGauge.Set(float64(balanceCents))
	m.liaGauge.Set(float64(liabilityCents))
	m.vltGauge.Set(float64(vaultCents))
	m.cusGauge.Set(float64(custodyCents))
	m.defGauge.Set(float64(liabilityCents - custodyCents))

	feeLamports, feeKnown := m.checkFees(ctx)

	m.mu.Lock()
	m.last = Reading{
		CustodyCents: custodyCents, HotCents: balanceCents,
		VaultCents: vaultCents, VaultKnown: vaultKnown,
		LiabilityCents: liabilityCents,
		FeeLamports:    feeLamports, FeeKnown: feeKnown,
		ObservedAt: time.Now().UTC(),
	}
	m.mu.Unlock()

	// Two distinct conditions that a single balance-vs-liability comparison conflates
	// once custody is split. Insolvency means the money to pay users is not there at
	// all; underfunding means it is there, in the vault, and a human has to move some
	// of it. Reporting the second as the first sends an operator hunting a theft that
	// did not happen — and on a split-custody deployment that is the NORMAL state.
	switch {
	case custodyCents < liabilityCents && vaultKnown:
		m.log.Error("payout: TREASURY SHORTFALL — on-chain USDC below outstanding withdrawal liability",
			"custody_cents", custodyCents, "hot_cents", balanceCents, "vault_cents", vaultCents,
			"liability_cents", liabilityCents, "shortfall_cents", liabilityCents-custodyCents, "ata", m.ata)
	case balanceCents < liabilityCents:
		m.log.Warn("payout: HOT WALLET UNDERFUNDED — top up the payout float from cold storage",
			"hot_cents", balanceCents, "liability_cents", liabilityCents,
			"needed_cents", liabilityCents-balanceCents, "vault_cents", vaultCents, "ata", m.ata)
	}

	// Over-funding is a quieter risk than a shortfall and has no natural alarm: nothing
	// breaks, users get paid, and the balance simply grows until a key compromise is
	// catastrophic instead of merely expensive. Reported against the CAP rather than
	// against liability, because the cap is the number an operator chose to accept.
	if m.exposureCapCents > 0 {
		excess := balanceCents - m.exposureCapCents
		if excess < 0 {
			excess = 0
		}
		m.expGauge.Set(float64(excess))
		if excess > 0 {
			m.log.Warn("payout: HOT WALLET OVER EXPOSURE CAP — sweep the excess to cold storage",
				"balance_cents", balanceCents, "cap_cents", m.exposureCapCents,
				"excess_cents", excess, "liability_cents", liabilityCents, "ata", m.ata,
				"sweep_to", m.coldAddress)
		}
	}
	return balanceCents, liabilityCents, nil
}

// checkFees reads the signing wallet's native SOL and alerts when it is too low to
// keep broadcasting. known is false when the check is unconfigured or the read failed,
// so callers never treat an unknown balance as an empty one.
//
// Never fatal to the pass: SOL running out is a problem, and not knowing whether it has
// run out is not a reason to also stop reporting solvency.
func (m *SolvencyMonitor) checkFees(ctx context.Context) (lamports int64, known bool) {
	if m.fees == nil || m.hotWallet == "" {
		return 0, false
	}
	lamports, err := m.fees.LamportBalance(ctx, m.hotWallet)
	if err != nil {
		m.log.Warn("payout: hot-wallet SOL balance unreadable", "wallet", m.hotWallet, "error", err)
		return 0, false
	}
	m.solGauge.Set(float64(lamports))
	if m.minFeeLamports > 0 && lamports < m.minFeeLamports {
		// ERROR, not WARN: below this line withdrawals do not degrade, they stop. The
		// USDC is all still there, which is exactly why no other check fires.
		m.log.Error("payout: HOT WALLET LOW ON SOL — payouts will fail to broadcast; fund the wallet with SOL",
			"wallet", m.hotWallet, "lamports", lamports, "min_lamports", m.minFeeLamports,
			"sol", float64(lamports)/lamportsPerSOL,
			"min_sol", float64(m.minFeeLamports)/lamportsPerSOL)
	}
	return lamports, true
}

// Run reconciles on a ticker until ctx is cancelled. A panic in a pass is recovered so
// the worker survives a malformed RPC response.
func (m *SolvencyMonitor) Run(every time.Duration) func(context.Context) {
	if every <= 0 {
		every = 5 * time.Minute
	}
	// Four missed passes is stale. Long enough that a single slow or failed pass does
	// not make approvals stop consulting the reading, short enough that a genuinely
	// dead monitor stops being treated as authoritative.
	m.mu.Lock()
	m.staleAfter = 4 * every
	m.mu.Unlock()
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
