package wallet

import (
	"context"

	"github.com/agent-arena/arena/internal/ledger"
)

// StakeMafiaTable escrows every seat's entry fee in one atomic transaction.
// Idempotency key stake:{match}. Implements mafia.Wallet via adapter in main.
func (s *Service) StakeMafiaTable(ctx context.Context, matchPublicID string, agents []string, entryFee int64) error {
	if len(agents) == 0 || entryFee <= 0 {
		return nil
	}
	postings := make([]ledger.Posting, 0, len(agents)+1)
	var gross int64
	for _, ag := range agents {
		postings = append(postings, ledger.Posting{Wallet: ledger.AgentWallet(ag), Amount: -entryFee})
		gross += entryFee
	}
	postings = append(postings, ledger.Posting{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: gross})
	res, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindStake,
		Key:      "stake:" + matchPublicID,
		Metadata: map[string]any{"match": matchPublicID, "agents": agents, "entry_fee": entryFee, "game": "mafia"},
		Postings: postings,
	})
	if err != nil {
		return err
	}
	if res.Applied {
		s.m.staked.Add(float64(gross))
	}
	return nil
}

// SettleMafiaTable pays alive winners plus platform rake from escrow.
// payouts maps agent public id → coin credit (excluding returned stake semantics).
func (s *Service) SettleMafiaTable(ctx context.Context, matchPublicID string, platformFee int64, payouts map[string]int64) error {
	allowed, err := s.gate.Allow(ctx, matchPublicID)
	if err != nil {
		return err
	}
	if !allowed {
		s.m.heldPayouts.Inc()
		return nil
	}

	set, err := s.repo.Settlement(ctx, matchPublicID)
	if err != nil {
		return err
	}
	gross := set.Bid * int64(len(set.Agents))

	postings := []ledger.Posting{{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: -gross}}
	if platformFee > 0 {
		postings = append(postings, ledger.Posting{Wallet: ledger.SystemWallet(ledger.SysPlatformRevenue), Amount: platformFee})
	}
	var paid int64
	for ag, amt := range payouts {
		if amt <= 0 {
			continue
		}
		postings = append(postings, ledger.Posting{Wallet: ledger.AgentWallet(ag), Amount: amt})
		paid += amt
	}
	// Remainder from floor division stays with platform revenue.
	remainder := gross - platformFee - paid
	if remainder > 0 {
		postings = append(postings, ledger.Posting{Wallet: ledger.SystemWallet(ledger.SysPlatformRevenue), Amount: remainder})
	}

	res, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindSettle,
		Key:      "settle:" + matchPublicID,
		Metadata: map[string]any{"match": matchPublicID, "game": "mafia", "platform_fee": platformFee, "gross": gross},
		Postings: postings,
	})
	if err != nil {
		return err
	}
	if res.Applied && platformFee > 0 {
		s.m.rake.Add(float64(platformFee))
	}
	return nil
}

// mafiaWallet adapts wallet.Service to mafia.Wallet.
type MafiaWallet struct{ s *Service }

func (w MafiaWallet) StakeTable(ctx context.Context, matchPublicID string, agents []string, entryFee int64) error {
	return w.s.StakeMafiaTable(ctx, matchPublicID, agents, entryFee)
}

func (w MafiaWallet) SettleTable(ctx context.Context, matchPublicID string, platformFee int64, payouts map[string]int64) error {
	return w.s.SettleMafiaTable(ctx, matchPublicID, platformFee, payouts)
}

func NewMafiaWallet(s *Service) MafiaWallet { return MafiaWallet{s: s} }
