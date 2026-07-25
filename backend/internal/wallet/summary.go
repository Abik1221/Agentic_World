package wallet

import (
	"context"
)

// UserSummary is the owner-facing financial dashboard (treasury + agents).
type UserSummary struct {
	User                string         `json:"user"`
	AvailableBalance    int64          `json:"available_balance"`
	LockedBalance       int64          `json:"locked_balance"`
	PendingBalance      int64          `json:"pending_balance"`
	LifetimeDeposits    int64          `json:"lifetime_deposits"`
	LifetimeWithdrawals int64          `json:"lifetime_withdrawals"`
	TournamentWinnings  int64          `json:"tournament_winnings"`
	LifetimeEarnings    int64          `json:"lifetime_earnings"`
	Agents              []AgentBalance `json:"agents"`
	CoinCents           int64          `json:"coin_cents"`
}

// AgentBalance is one agent's competition wallet snapshot.
type AgentBalance struct {
	Agent           string `json:"agent"`
	Name            string `json:"name"`
	Balance         int64  `json:"balance"`
	LockedInMatches int64  `json:"locked_in_matches"`
	Withdrawable    int64  `json:"withdrawable"`
	ActiveMatches   int    `json:"active_matches"`
}

// LifetimeStats aggregates immutable ledger facts for an owner.
type LifetimeStats struct {
	Deposits    int64
	Withdrawals int64
	Winnings    int64
}

// AgentRow is a lightweight agent listing for the owner dashboard.
type AgentRow struct {
	PublicID string
	Name     string
}

// UserSummary assembles the owner treasury view and per-agent balances.
func (s *Service) UserSummary(ctx context.Context, userPublicID string) (UserSummary, error) {
	avail, err := s.ledger.UserBalance(ctx, userPublicID)
	if err != nil {
		return UserSummary{}, err
	}
	stats, err := s.repo.UserLifetimeStats(ctx, userPublicID)
	if err != nil {
		return UserSummary{}, err
	}
	agents, err := s.repo.OwnerAgents(ctx, userPublicID)
	if err != nil {
		return UserSummary{}, err
	}

	var locked, pending int64
	outAgents := make([]AgentBalance, 0, len(agents))
	for _, ag := range agents {
		bal, err := s.ledger.Balance(ctx, ag.PublicID)
		if err != nil {
			return UserSummary{}, err
		}
		matchLock, err := s.repo.StakedInActiveMatches(ctx, ag.PublicID)
		if err != nil {
			return UserSummary{}, err
		}
		pend, err := s.repo.PendingWithdrawalCoins(ctx, ag.PublicID)
		if err != nil {
			return UserSummary{}, err
		}
		withdrawable, err := s.repo.WithdrawableCoins(ctx, ag.PublicID)
		if err != nil {
			return UserSummary{}, err
		}
		active, err := s.repo.ActiveMatchCount(ctx, ag.PublicID)
		if err != nil {
			return UserSummary{}, err
		}
		locked += matchLock + pend
		pending += pend
		outAgents = append(outAgents, AgentBalance{
			Agent: ag.PublicID, Name: ag.Name, Balance: bal,
			LockedInMatches: matchLock, Withdrawable: withdrawable, ActiveMatches: active,
		})
	}

	return UserSummary{
		User:                userPublicID,
		AvailableBalance:    avail,
		LockedBalance:       locked,
		PendingBalance:      pending,
		LifetimeDeposits:    stats.Deposits,
		LifetimeWithdrawals: stats.Withdrawals,
		TournamentWinnings:  stats.Winnings,
		LifetimeEarnings:    stats.Winnings,
		Agents:              outAgents,
		CoinCents:           s.cfg.CoinCents,
	}, nil
}

// UserHistory returns treasury ledger lines for the owner.
func (s *Service) UserHistory(ctx context.Context, userPublicID string, limit, offset int) ([]HistoryLine, int, error) {
	lim := effLimit(limit)
	lines, err := s.ledger.UserHistory(ctx, userPublicID, lim, maxInt(offset, 0))
	if err != nil {
		return nil, 0, err
	}
	return toHistoryLines(lines, offset, lim)
}
