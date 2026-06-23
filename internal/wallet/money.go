package wallet

import (
	"context"

	"github.com/agent-arena/arena/internal/ledger"
)

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
	}
	return nil
}

// RefundStakes returns both bids of a match that was staked but never activated.
// Idempotency key refund:{match}. Implements match.Wallet.
func (s *Service) RefundStakes(ctx context.Context, matchPublicID, agentA, agentB string, bid int64) error {
	_, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindRefund,
		Key:      "refund:" + matchPublicID,
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

// SettleHeld pays out a match whose hold an admin has cleared, re-deriving the
// winner/pool/rake from persisted state. The gate is intentionally bypassed.
func (s *Service) SettleHeld(ctx context.Context, matchPublicID string) error {
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
		Key:      "settle:" + matchPublicID,
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

// Refund returns every stake of a match to its player (used for aborts). The
// pool is reconstructed from the match's stakeholders. Idempotency key
// refund:{match}. Implements match.Wallet.
//
// Refund and Settle are mutually exclusive for a given match (a finished match
// settles; only a never-finished match aborts), so their distinct keys never
// double-pay.
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
		Key:      "refund:" + matchPublicID,
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

// Topup credits coins to an agent from stripe_clearing after a settled Stripe
// payment. Idempotency key topup:{stripe_event_id} makes webhook redelivery a
// no-op. Implements payments.Coiner.
func (s *Service) Topup(ctx context.Context, agentPublicID string, coins int64, idemKey string) error {
	return s.credit(ctx, agentPublicID, coins, idemKey, "stripe")
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
		_ = s.repo.RepayDebt(ctx, agentPublicID, repay)
	}
	return nil
}

// Reverse claws back a previously credited top-up on a Stripe refund/chargeback.
// A wallet may never go negative, so it recovers what the agent still holds and
// records the SHORTFALL as bad debt (a balanced double-entry into bad_debt + a
// per-agent receivable). Idempotency key reversal:{event}. Implements payments.Coiner.
func (s *Service) Reverse(ctx context.Context, agentPublicID string, coins int64, idemKey string) error {
	bal, err := s.ledger.Balance(ctx, agentPublicID)
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

	// coins removed from circulation; recovered from the agent; the rest booked as debt.
	postings := []ledger.Posting{{Wallet: ledger.SystemWallet(ledger.SysStripeClearing), Amount: coins}}
	if recovered > 0 {
		postings = append(postings, ledger.Posting{Wallet: ledger.AgentWallet(agentPublicID), Amount: -recovered})
	}
	if shortfall > 0 {
		postings = append(postings, ledger.Posting{Wallet: ledger.SystemWallet(ledger.SysBadDebt), Amount: -shortfall})
	}

	res, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindReversal,
		Key:      idemKey,
		Metadata: map[string]any{"agent": agentPublicID, "coins": coins, "recovered": recovered, "debt": shortfall},
		Postings: postings,
	})
	if err != nil {
		return err
	}
	if res.Applied && shortfall > 0 {
		_ = s.repo.RecordDebt(ctx, agentPublicID, shortfall)
		s.m.chargebackDebt.Add(float64(shortfall))
	}
	return nil
}
