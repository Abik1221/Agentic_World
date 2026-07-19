package wallet

import (
	"context"

	"github.com/agent-arena/arena/internal/ledger"
)

// monopoly.go mirrors mafia.go for Monopoly: agent-vs-agent, entry-fee pooling.
// Every seated agent stakes the entry fee into escrow at match start; the winning
// seat takes the reward pool and the platform takes its rake at finish. Unlike
// mafia this path does NOT route through the anti-fraud hold gate yet — settlement
// is direct + idempotent; a held-review/replay parity pass is a follow-up.

// StakeMonopolyTable escrows every seated agent's entry fee in one atomic txn.
// Idempotency key stake:{match}. Implements monopoly.Wallet.StakeTable via adapter.
func (s *Service) StakeMonopolyTable(ctx context.Context, matchPublicID string, agents []string, entryFee int64) error {
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
		Metadata: map[string]any{"match": matchPublicID, "agents": agents, "entry_fee": entryFee, "game": "monopoly"},
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

// SettleMonopolyTable pays the winning seat its reward pool plus the platform rake
// from escrow. `gross` is the full staked pool (agents × entryFee); any floor-division
// remainder after payouts + fee stays with platform revenue so escrow always zeroes
// out. Idempotent via the shared settle:{match} key.
func (s *Service) SettleMonopolyTable(ctx context.Context, matchPublicID string, gross, platformFee int64, payouts map[string]int64) error {
	if gross <= 0 {
		return nil // practice table — nothing to move
	}
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
	// Remainder (floor division, or a pool with no eligible agent winner) stays with
	// the platform so the escrow debit is always fully balanced.
	remainder := gross - platformFee - paid
	if remainder > 0 {
		postings = append(postings, ledger.Posting{Wallet: ledger.SystemWallet(ledger.SysPlatformRevenue), Amount: remainder})
	}
	res, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindSettle,
		Key:      disburseKey(matchPublicID),
		Metadata: map[string]any{"match": matchPublicID, "game": "monopoly", "platform_fee": platformFee, "gross": gross},
		Postings: postings,
	})
	if err != nil {
		return err
	}
	if res.Applied {
		if rake := platformFee + remainder; rake > 0 {
			s.m.rake.Add(float64(rake))
		}
	}
	return nil
}

// RefundMonopolyTable unwinds a staked table (each agent's entry fee returned from
// escrow) when start fails after staking. Idempotent via the shared disburse:{match}
// key, so a refund and a settle can never both apply to the same match.
func (s *Service) RefundMonopolyTable(ctx context.Context, matchPublicID string, agents []string, entryFee int64) error {
	if len(agents) == 0 || entryFee <= 0 {
		return nil
	}
	postings := make([]ledger.Posting, 0, len(agents)+1)
	var gross int64
	for _, ag := range agents {
		postings = append(postings, ledger.Posting{Wallet: ledger.AgentWallet(ag), Amount: entryFee})
		gross += entryFee
	}
	postings = append(postings, ledger.Posting{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: -gross})
	_, err := s.ledger.Post(ctx, ledger.Txn{
		Kind:     ledger.KindRefund,
		Key:      disburseKey(matchPublicID),
		Metadata: map[string]any{"match": matchPublicID, "game": "monopoly", "reason": "activation_failed"},
		Postings: postings,
	})
	return err
}

// MonopolyWallet adapts wallet.Service to monopoly.Wallet.
type MonopolyWallet struct{ s *Service }

func (w MonopolyWallet) StakeTable(ctx context.Context, matchPublicID string, agents []string, entryFee int64) error {
	return w.s.StakeMonopolyTable(ctx, matchPublicID, agents, entryFee)
}

func (w MonopolyWallet) SettleTable(ctx context.Context, matchPublicID string, gross, platformFee int64, payouts map[string]int64) error {
	return w.s.SettleMonopolyTable(ctx, matchPublicID, gross, platformFee, payouts)
}

func (w MonopolyWallet) RefundTable(ctx context.Context, matchPublicID string, agents []string, entryFee int64) error {
	return w.s.RefundMonopolyTable(ctx, matchPublicID, agents, entryFee)
}

func NewMonopolyWallet(s *Service) MonopolyWallet { return MonopolyWallet{s: s} }
