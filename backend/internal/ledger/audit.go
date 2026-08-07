package ledger

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Ledger integrity audit.
//
// # Why this exists as code and not as a query someone runs
//
// The double-entry invariants held when I checked them by hand: 957 transactions, 2864 entries,
// 152 wallets, nothing unbalanced and nothing drifted. That is a statement about one afternoon.
// The property that matters for a platform holding real coins is that a violation is DETECTED,
// and an invariant nobody checks on a schedule is an invariant that will be discovered by a user
// whose balance is wrong.
//
// The checks are deliberately independent of the code that writes the ledger. They recompute from
// the entries rather than trusting any in-process accounting, so a bug in the writer cannot
// silence the audit that would catch it — a self-attesting check is worth nothing.
//
// # What each finding would mean
//
// Unbalanced transaction: coins were created or destroyed by a single posting. This is the
// gravest one; a payout could be funded from nowhere.
//
// Balance drift: a wallet's stored balance disagrees with its own entries. The entries are the
// record of truth, so drift means the cached figure a user SEES is wrong even when the underlying
// history is right.
//
// Negative agent or escrow balance: the schema forbids it, so a row here means the constraint was
// bypassed — by a direct write, or by a migration that dropped and did not restore it.
//
// Non-zero global sum: coins in the system as a whole are not conserved. Legitimate only if a
// mint/burn wallet exists to absorb it, which is why the house wallet kinds are reported
// separately rather than folded in.
type AuditFinding struct {
	Check string `json:"check"`
	// Count is how many rows violate the check. Zero is the healthy value for every check here.
	Count int64 `json:"count"`
	// Detail carries the first offending identifiers, so an alert is actionable without a
	// follow-up query. Capped: an audit that dumps ten thousand ids into a log is one nobody reads.
	Detail string `json:"detail,omitempty"`
	// Severity separates "coins are wrong" from "a cached number is wrong".
	Severity string `json:"severity"`
}

const (
	SeverityCritical = "critical" // coins created, destroyed, or unaccounted for
	SeverityHigh     = "high"     // stored state disagrees with the entry history
)

// AuditReport is the whole picture. Healthy means every check returned zero.
type AuditReport struct {
	Healthy      bool           `json:"healthy"`
	Findings     []AuditFinding `json:"findings"`
	Transactions int64          `json:"transactions"`
	Entries      int64          `json:"entries"`
	Wallets      int64          `json:"wallets"`

	// Escrow reconciliation. Reported on EVERY run, healthy or not.
	//
	// This exists because a large escrow balance has two completely different meanings and the
	// ledger invariants cannot tell them apart: coins conserved perfectly while 46,700 sat in
	// escrow, because the antifraud gate was holding payouts exactly as designed. Nothing logged
	// it. The only way to learn that the platform was sitting on held money was to query the
	// database and work backwards, which is not a thing anyone does before a user complains.
	//
	// So the held figure is published as NORMAL OUTPUT, not as a finding: a hold is correct
	// behaviour and a review queue is a workload, not a defect. What IS a defect is escrow that
	// no open match and no recorded hold can account for — that is money the platform has taken
	// and has no story for.
	EscrowBalance int64 `json:"escrow_balance"`
	EscrowHeld    int64 `json:"escrow_held"`
	EscrowOpen    int64 `json:"escrow_open"`
}

// Auditor is the read side of the audit. Satisfied by *store.LedgerRepo.
//
// A separate port from Repo on purpose: Repo is the WRITE path, and an audit that reached through
// the same interface that posts transactions would invite an implementation that shares its
// caching or its in-process bookkeeping. The check has to be able to disagree with the writer.
type Auditor interface {
	AuditLedger(ctx context.Context) (AuditReport, error)
}

// Audit recomputes the ledger invariants from the entries.
//
// Read-only, and safe against a live database: every statement is an aggregate over committed
// rows. It takes no locks and repairs nothing — an audit that also fixed things would destroy the
// evidence needed to work out how the imbalance happened.
func (s *Service) Audit(ctx context.Context) (AuditReport, error) {
	a, ok := s.repo.(Auditor)
	if !ok {
		return AuditReport{}, fmt.Errorf("ledger audit: repo does not support auditing")
	}
	return a.AuditLedger(ctx)
}

// AuditWorker re-runs the integrity audit on an interval.
//
// # Why a worker and not just an endpoint
//
// An endpoint is only as good as the habit of calling it, and nobody curls a health check at
// 3am. A ledger imbalance is silent by nature — balances still add up per-wallet, the UI still
// renders, and the first external symptom is a user disputing a payout. The whole value is in
// finding it before they do.
//
// # Why it never blocks or repairs
//
// The audit is read-only and the worker only reports. A process that tried to "fix" an imbalance
// would be writing to the ledger on the basis of a computation that has just proved the ledger
// cannot be trusted, and it would destroy the evidence of how the imbalance arose. The correct
// response to a critical finding is a human reading the entries, so the worker's job is to make
// sure a human is told.
type AuditWorker struct {
	svc      *Service
	interval time.Duration
	log      *slog.Logger
	// onFinding fires for each violation, so a deployment can page rather than only log. Optional.
	onFinding func(AuditFinding)
}

func NewAuditWorker(svc *Service, interval time.Duration, log *slog.Logger) *AuditWorker {
	if log == nil {
		log = slog.Default()
	}
	return &AuditWorker{svc: svc, interval: interval, log: log}
}

// OnFinding registers an alert sink invoked once per violation per run.
func (w *AuditWorker) OnFinding(f func(AuditFinding)) { w.onFinding = f }

// Run audits until ctx is cancelled, once immediately on start.
//
// The immediate run matters: an imbalance introduced by the deploy that just happened is exactly
// the one worth catching, and waiting a full interval to look would let it ship quietly.
func (w *AuditWorker) Run(ctx context.Context) {
	w.log.Info("ledger audit worker started", "interval", w.interval.String())
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		rep, err := w.svc.Audit(ctx)
		switch {
		case err != nil:
			// Logged, not fatal. An audit that cannot run is not evidence of an imbalance, and
			// killing the server over a failed read would turn a monitoring problem into an
			// outage.
			w.log.Error("ledger audit could not run", "error", err)
		case rep.Healthy:
			// Escrow figures on the clean path too. Held money is not a defect, but it is a
			// WORKLOAD, and one nobody can see is one nobody works — which is how a review queue
			// becomes a pile of coins the platform is quietly sitting on.
			w.log.Info("ledger audit clean",
				"transactions", rep.Transactions, "entries", rep.Entries, "wallets", rep.Wallets,
				"escrow_balance", rep.EscrowBalance, "escrow_held_for_review", rep.EscrowHeld,
				"escrow_open_matches", rep.EscrowOpen)
		default:
			for _, f := range rep.Findings {
				// ERROR level for every finding, including the "high" ones: a wallet whose stored
				// balance disagrees with its entries is a number a user is being shown, and there
				// is no severity of wrong-balance that belongs at info.
				w.log.Error("LEDGER INTEGRITY VIOLATION",
					"check", f.Check, "severity", f.Severity, "rows", f.Count, "ids", f.Detail)
				if w.onFinding != nil {
					w.onFinding(f)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
