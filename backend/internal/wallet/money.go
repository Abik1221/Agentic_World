package wallet

import (
	"context"

	"github.com/agent-arena/arena/internal/ledger"
	"github.com/agent-arena/arena/internal/paymenttrace"
)

// disburseKey is the SHARED idempotency key for EVERY escrow-OUT of a match —
// settle, tie-refund, abort refund, activation-failed refund, and held release/
// refund. A match's escrow must be paid out EXACTLY ONCE. Because the ledger
// enforces UNIQUE(idempotency_key), routing all disbursements through this single
// key makes any second attempt a safe no-op (ApplyResult.Applied=false), which
// closes the settle-then-dispute-refund (and release+refund) escrow double-spend:
// the shared escrow wallet can never be debited twice for the same match. Settle vs
// refund carry different ledger Kinds, but idempotency is keyed on the key alone, so
// whichever disbursement runs first wins and the rest no-op. (H2)
func disburseKey(matchPublicID string) string { return "disburse:" + matchPublicID }

// StakeMatch escrows BOTH seats' bids in ONE balanced, atomic transaction:
// agentA −bid, agentB −bid, escrow +2·bid. Either both stake or neither — a seat's
// coins can never be orphaned in escrow by a partial failure. Idempotency key
// stake:{match}. Implements match.Wallet.
func (s *Service) StakeMatch(ctx context.Context, matchPublicID, agentA, agentB string, bid int64) error {
	res, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindStake,
		Key:      "stake:" + matchPublicID,
		Metadata: map[string]any{"match": matchPublicID, "agents": []string{agentA, agentB}, "bid": bid},
		Postings: []ledger.Posting{
			{Wallet: ledger.AgentWallet(agentA), Amount: -bid},
			{Wallet: ledger.AgentWallet(agentB), Amount: -bid},
			{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: 2 * bid},
		},
	})
	if err != nil {
		return err
	}
	if res.Applied {
		s.m.staked.Add(float64(2 * bid))
		// Only on Applied: a re-stake of the same match is a ledger no-op, so pushing
		// again would tell the owner their coins left twice.
		s.signalAgentOwners(eventCoinsStaked, "stake:"+matchPublicID,
			map[string]any{"match": matchPublicID, "coins": bid}, agentA, agentB)
	}
	return nil
}

// RefundStakes returns both bids of a match that was staked but never activated.
// Idempotency key refund:{match}. Implements match.Wallet.
func (s *Service) RefundStakes(ctx context.Context, matchPublicID, agentA, agentB string, bid int64) error {
	_, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindRefund,
		Key:      disburseKey(matchPublicID),
		Metadata: map[string]any{"match": matchPublicID, "reason": "activation_failed"},
		Postings: []ledger.Posting{
			{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: -2 * bid},
			{Wallet: ledger.AgentWallet(agentA), Amount: bid},
			{Wallet: ledger.AgentWallet(agentB), Amount: bid},
		},
	})
	return err
}

// Settle pays out a finished match — UNLESS the payout gate places a hold (Stage
// 9 anti-fraud). On a hold the stakes stay in escrow and no coins move; an admin
// later releases (SettleHeld) or refunds. Implements match.Wallet.
func (s *Service) Settle(ctx context.Context, matchPublicID, winnerAgentPublicID string, pool int64, rakePct int) error {
	allowed, err := s.gate.Allow(ctx, matchPublicID)
	if err != nil {
		return err
	}
	if !allowed {
		s.m.heldPayouts.Inc()
		return nil // held: escrow retained, settlement deferred to admin review
	}
	return s.settle(ctx, matchPublicID, winnerAgentPublicID, pool, rakePct)
}

// SettleHeld pays out a match whose hold an admin has cleared. A multi-winner game
// (Mafia) persisted its computed per-seat split when it was held, so replay that
// exactly; only a plain 2-player match (Goofspiel) with no persisted split falls
// back to winner-take-all. Paying a Mafia pot winner-take-all here would over-pay
// one seat and under-pay the surviving team. The gate is intentionally bypassed.
func (s *Service) SettleHeld(ctx context.Context, matchPublicID string) error {
	fee, payouts, found, err := s.repo.HeldSettlement(ctx, matchPublicID)
	if err != nil {
		return err
	}
	if found {
		// A held Monopoly table carries its gross in a sentinel entry (Monopoly has no
		// matches.bid to derive it from). Detect + recover it, then replay the split.
		if gross, ok := payouts[heldMonopolyGrossKey]; ok {
			delete(payouts, heldMonopolyGrossKey)
			return s.settleMonopoly(ctx, matchPublicID, gross, fee, payouts)
		}
		return s.settleMafia(ctx, matchPublicID, fee, payouts)
	}
	set, err := s.repo.Settlement(ctx, matchPublicID)
	if err != nil {
		return err
	}
	pool := set.Bid * int64(len(set.Agents))
	return s.settle(ctx, matchPublicID, set.Winner, pool, set.RakePct)
}

// settle is the gate-free core: escrow → winner (+rake) or tie/refund split.
// Idempotency key settle:{match} makes any re-invocation a no-op.
func (s *Service) settle(ctx context.Context, matchPublicID, winnerAgentPublicID string, pool int64, rakePct int) error {
	postings := []ledger.Posting{{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: -pool}}
	var rake int64

	if winnerAgentPublicID == "" {
		// Tie: hand each player their stake back. The pool is the sum of stakes.
		set, err := s.repo.Settlement(ctx, matchPublicID)
		if err != nil {
			return err
		}
		for _, ag := range set.Agents {
			postings = append(postings, ledger.Posting{Wallet: ledger.AgentWallet(ag), Amount: set.Bid})
		}
	} else {
		rake = pool * int64(rakePct) / 100
		postings = append(postings,
			ledger.Posting{Wallet: ledger.AgentWallet(winnerAgentPublicID), Amount: pool - rake},
			ledger.Posting{Wallet: ledger.SystemWallet(ledger.SysPlatformRevenue), Amount: rake},
		)
	}

	res, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindSettle,
		Key:      disburseKey(matchPublicID),
		Metadata: map[string]any{"match": matchPublicID, "winner": winnerAgentPublicID, "pool": pool, "rake": rake},
		Postings: postings,
	})
	if err != nil {
		return err
	}
	if res.Applied && rake > 0 {
		s.m.rake.Add(float64(rake))
	}
	return nil
}

// Refund returns every stake of a match to its player (used for aborts and
// dispute-refunds). The pool is reconstructed from the match's stakeholders.
//
// Refund and Settle share ONE per-match disbursement key (disburseKey), so they
// are mutually exclusive by construction: whichever disburses the match's escrow
// first wins and the other is a ledger no-op. This is what prevents a dispute-
// refund of an already-settled match from debiting the shared escrow twice. (H2)
func (s *Service) Refund(ctx context.Context, matchPublicID string) error {
	set, err := s.repo.Settlement(ctx, matchPublicID)
	if err != nil {
		return err
	}
	if len(set.Agents) == 0 {
		return nil // nothing was ever staked
	}
	pool := set.Bid * int64(len(set.Agents))
	postings := []ledger.Posting{{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: -pool}}
	for _, ag := range set.Agents {
		postings = append(postings, ledger.Posting{Wallet: ledger.AgentWallet(ag), Amount: set.Bid})
	}
	_, err = s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindRefund,
		Key:      disburseKey(matchPublicID),
		Metadata: map[string]any{"match": matchPublicID, "pool": pool},
		Postings: postings,
	})
	return err
}

// Mint grants coins to an agent from the stripe_clearing system wallet. It is a
// non-prod test affordance only (Stage 5's Stripe top-ups are the real path).
// Each call is a distinct, non-idempotent transaction.
func (s *Service) Mint(ctx context.Context, agentPublicID string, amount int64, idemKey string) error {
	return s.credit(ctx, agentPublicID, amount, idemKey, "mint")
}

// Topup credits coins to the owner's treasury after a settled Stripe payment.
// Idempotency key topup:{session_id} makes webhook redelivery a no-op.
func (s *Service) Topup(ctx context.Context, userPublicID string, coins int64, idemKey string) error {
	return s.creditUser(ctx, userPublicID, coins, idemKey, "stripe")
}

// CreditDeposit credits coins to the owner's treasury after a CONFIRMED on-chain
// stablecoin deposit (Solana USDC), taking the platform deposit fee: external
// clearing → user treasury (userCoins) + platform_revenue (feeCoins), one balanced
// txn. Tagged source "solana" for the ledger audit trail. Idempotent on idemKey
// (use "solana:<tx_signature>") so a re-observed transfer is a no-op.
func (s *Service) CreditDeposit(ctx context.Context, userPublicID string, userCoins, feeCoins int64, idemKey string) error {
	if userCoins <= 0 && feeCoins <= 0 {
		return nil // nothing to credit (sub-coin deposit)
	}
	postings := []ledger.Posting{
		{Wallet: ledger.SystemWallet(ledger.SysStripeClearing), Amount: -(userCoins + feeCoins)},
		{Wallet: ledger.UserWallet(userPublicID), Amount: userCoins},
	}
	if feeCoins > 0 {
		postings = append(postings, ledger.Posting{Wallet: ledger.SystemWallet(ledger.SysPlatformRevenue), Amount: feeCoins})
	}
	_, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindTopup,
		Key:      idemKey,
		Metadata: map[string]any{"user": userPublicID, "coins": userCoins, "fee": feeCoins, "source": "solana"},
		Postings: postings,
	})
	return err
}

// creditUser applies incoming coins to the owner's treasury wallet.
//
// This is the single choke point for every treasury credit that is NOT the Solana
// deposit rail: a settled card top-up, a subscription grant. Both arrive by
// webhook, minutes after the user finished paying and possibly on a different
// page — the textbook "I paid and nothing happened" case. Neither service wrote a
// notification, so the confirmation is raised here, where all of them pass,
// rather than being added to each caller and forgotten by the next one.
func (s *Service) creditUser(ctx context.Context, userPublicID string, coins int64, idemKey, source string) error {
	postings := []ledger.Posting{
		{Wallet: ledger.SystemWallet(ledger.SysStripeClearing), Amount: -coins},
		{Wallet: ledger.UserWallet(userPublicID), Amount: coins},
	}
	res, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindTopup,
		Key:      idemKey,
		Metadata: map[string]any{"user": userPublicID, "coins": coins, "source": source},
		Postings: postings,
	})
	if err != nil {
		return err
	}
	// Applied only: webhook redelivery is a ledger no-op and must be a UI no-op too.
	// The idempotency key doubles as the notification ref, so even a push that
	// somehow escapes this guard cannot create a second row.
	if res.Applied {
		// The provider settling is the only "before" this flow has — everything
		// earlier happened inside Stripe. Recording it explicitly gives the diagram a
		// first node, so a top-up that credited can be told apart from one whose
		// webhook never arrived.
		s.trace.OK(ctx, userPublicID, paymenttrace.FlowTopup, idemKey,
			paymenttrace.StagePaymentReceived, map[string]any{"source": source})
		s.trace.OK(ctx, userPublicID, paymenttrace.FlowTopup, idemKey,
			paymenttrace.StageTopupCredited, map[string]any{"coins": coins, "source": source})
		s.notifyUser(userPublicID, eventCoinsToppedUp, idemKey,
			paymenttrace.FlowTopup, paymenttrace.StageTopupNotified,
			map[string]any{"coins": coins, "source": source})
	}
	return nil
}

// AdminAdjust applies a Super Admin manual balance adjustment to the owner's
// treasury: coins > 0 credits, coins < 0 debits (a debit that would take the
// wallet negative is rejected by the ledger's non-negative constraint). Balanced
// against the external clearing account and idempotent on idemKey. Callers MUST
// audit this out-of-band.
func (s *Service) AdminAdjust(ctx context.Context, userPublicID string, coins int64, idemKey, reason string) error {
	postings := []ledger.Posting{
		{Wallet: ledger.SystemWallet(ledger.SysStripeClearing), Amount: -coins},
		{Wallet: ledger.UserWallet(userPublicID), Amount: coins},
	}
	res, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindAdjust,
		Key:      idemKey,
		Metadata: map[string]any{"user": userPublicID, "coins": coins, "reason": reason, "source": "admin"},
		Postings: postings,
	})
	if err != nil {
		return err
	}
	// An operator moving someone's balance is the one change a user has no way to
	// explain to themselves. Silent, it is indistinguishable from money going
	// missing. It gets a durable notice carrying the amount and the stated reason.
	if res.Applied {
		// No trace flow: an adjustment is a single instantaneous act by an operator,
		// not a multi-stage journey, and inventing a one-node diagram for it would
		// only dilute the ones that mean something.
		s.notifyUser(userPublicID, eventBalanceAdjusted, idemKey, "", "",
			map[string]any{"coins": coins, "reason": reason})
	}
	return nil
}

// credit applies incoming coins, repaying any outstanding chargeback debt FIRST:
// of `coins`, up to the debt is diverted to bad_debt (reducing the receivable) and
// only the remainder reaches the agent's wallet. One balanced, idempotent txn.
func (s *Service) credit(ctx context.Context, agentPublicID string, coins int64, idemKey, source string) error {
	debt, err := s.repo.OutstandingDebt(ctx, agentPublicID)
	if err != nil {
		return err
	}
	repay := coins
	if debt < repay {
		repay = debt
	}
	if repay < 0 {
		repay = 0
	}
	toWallet := coins - repay

	postings := []ledger.Posting{{Wallet: ledger.SystemWallet(ledger.SysStripeClearing), Amount: -coins}}
	if repay > 0 {
		postings = append(postings, ledger.Posting{Wallet: ledger.SystemWallet(ledger.SysBadDebt), Amount: repay})
	}
	if toWallet > 0 {
		postings = append(postings, ledger.Posting{Wallet: ledger.AgentWallet(agentPublicID), Amount: toWallet})
	}

	res, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindTopup,
		Key:      idemKey,
		Metadata: map[string]any{"agent": agentPublicID, "coins": coins, "source": source, "debt_repaid": repay},
		Postings: postings,
	})
	if err != nil {
		return err
	}
	if res.Applied && repay > 0 {
		bestEffortDebt(func() error { return s.repo.RepayDebt(ctx, agentPublicID, repay) })
	}
	return nil
}

// RecordFraudDebt records fraudulently-obtained winnings (e.g. a collusion clawback)
// as debt against an agent: it gates the agent's next cash-out (payout debt-gate)
// and is repaid FIRST out of any future credit (see credit). It is a proportionate,
// soft recovery — it does not seize the agent's current balance. The caller ensures
// it runs at-most-once per flag (antifraud records it only on a newly-created flag).
func (s *Service) RecordFraudDebt(ctx context.Context, agentPublicID string, coins int64, reason string) error {
	if coins <= 0 || agentPublicID == "" {
		return nil
	}
	return s.repo.RecordDebt(ctx, agentPublicID, coins)
}

// bestEffortDebt runs a per-agent debts-table adjustment with a bounded retry. The
// ledger (bad_debt) is authoritative for accounting; the debts counter only drives
// the payout gate, so a transient failure is safe-side — it over-blocks a payout
// (never loses money) and is reconcilable. We retry to close the transient window
// rather than swallow a single attempt, but never fail the money op on it.
func bestEffortDebt(fn func() error) {
	for i := 0; i < 3; i++ {
		if err := fn(); err == nil {
			return
		}
	}
}

// Reverse claws back a previously credited top-up on a Stripe refund/chargeback.
// Any shortfall (coins already spent, so the treasury can't cover the full claw)
// is booked to bad debt in the ledger AND recorded against agentPublicID so the
// payout debt-gate blocks that agent's next cash-out until the debt is repaid.
func (s *Service) Reverse(ctx context.Context, userPublicID, agentPublicID string, coins int64, idemKey string) error {
	bal, err := s.ledger.UserBalance(ctx, userPublicID)
	if err != nil {
		return err
	}
	recovered := coins
	if bal < recovered {
		recovered = bal
	}
	if recovered < 0 {
		recovered = 0
	}
	shortfall := coins - recovered

	postings := []ledger.Posting{{Wallet: ledger.SystemWallet(ledger.SysStripeClearing), Amount: coins}}
	if recovered > 0 {
		postings = append(postings, ledger.Posting{Wallet: ledger.UserWallet(userPublicID), Amount: -recovered})
	}
	if shortfall > 0 {
		postings = append(postings, ledger.Posting{Wallet: ledger.SystemWallet(ledger.SysBadDebt), Amount: -shortfall})
	}

	res, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindReversal,
		Key:      idemKey,
		Metadata: map[string]any{"user": userPublicID, "agent": agentPublicID, "coins": coins, "recovered": recovered, "debt": shortfall},
		Postings: postings,
	})
	if err != nil {
		return err
	}
	// Only on first application (res.Applied) so a redelivered reversal doesn't
	// double-count the debt; and only when we know which agent to attribute it to.
	if res.Applied && shortfall > 0 {
		s.m.chargebackDebt.Add(float64(shortfall))
		if agentPublicID != "" {
			// Per-agent debt gates that agent's next payout. Best-effort (bounded
			// retry): the ledger has already booked the bad debt authoritatively, so
			// a failure here (recoverable by reconciliation) must not fail the clawback.
			ag := agentPublicID
			amt := shortfall
			bestEffortDebt(func() error { return s.repo.RecordDebt(ctx, ag, amt) })
		}
	}
	return nil
}
