package wallet

import (
	"context"
	"fmt"
	"time"
)

// CheckJoin enforces the seven server-enforced spending limits, in order. Any
// failure returns a specific *httpx.APIError (402 for funds, 409 for policy) and
// moves zero coins. Implements match.Limits.
//
// The limits are owner-configured columns on the agent; an agent credential can
// never change them (enforced by the scope firewall in identity), so a runaway
// or compromised agent cannot widen its own leash.
// LossToday returns coins the agent has lost since the start of the current day
// (same boundary as the daily_loss_limit guardrail) — the figure the auto-play
// daily loss-stop reads.
func (s *Service) LossToday(ctx context.Context, agentPublicID string) (int64, error) {
	now := s.clock.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return s.repo.LossSince(ctx, agentPublicID, dayStart)
}

// NetToday returns the agent's net coin change since the start of the current day
// (wins − losses) — the figure the auto-play take-profit reads.
func (s *Service) NetToday(ctx context.Context, agentPublicID string) (int64, error) {
	now := s.clock.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return s.repo.NetSince(ctx, agentPublicID, dayStart)
}

func (s *Service) CheckJoin(ctx context.Context, agentPublicID string, bid int64) error {
	if bid <= 0 {
		return ErrInvalidBid
	}
	lim, err := s.repo.AgentLimits(ctx, agentPublicID)
	if err != nil {
		return err
	}
	bal, err := s.ledger.Balance(ctx, agentPublicID)
	if err != nil {
		return err
	}
	now := s.clock.Now()

	// 1. balance ≥ bid + min_wallet_balance
	if bal < bid+lim.MinWalletBalance {
		s.m.limitBlock.WithLabelValues(limitMinBalance).Inc()
		return blockBalance(
			fmt.Sprintf("Balance %d is below the required %d (bid %d + reserve %d).", bal, bid+lim.MinWalletBalance, bid, lim.MinWalletBalance),
			map[string]any{"balance": bal, "bid": bid, "min_wallet_balance": lim.MinWalletBalance})
	}
	// 2. bid ≤ coin_limit_per_match
	if bid > lim.CoinLimitPerMatch {
		s.m.limitBlock.WithLabelValues(limitPerMatch).Inc()
		return block(limitPerMatch, fmt.Sprintf("Bid %d exceeds the per-match limit of %d.", bid, lim.CoinLimitPerMatch),
			map[string]any{"bid": bid, "max": lim.CoinLimitPerMatch})
	}
	// 3. today's losses < daily_loss_limit
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	daily, err := s.repo.LossSince(ctx, agentPublicID, dayStart)
	if err != nil {
		return err
	}
	if daily >= lim.DailyLossLimit {
		s.m.limitBlock.WithLabelValues(limitDailyLoss).Inc()
		return block(limitDailyLoss, fmt.Sprintf("Daily loss %d has reached the limit of %d.", daily, lim.DailyLossLimit),
			map[string]any{"loss_today": daily, "max": lim.DailyLossLimit})
	}
	// 4. session losses < session_loss_limit
	session, err := s.repo.LossSince(ctx, agentPublicID, now.Add(-s.cfg.SessionWindow))
	if err != nil {
		return err
	}
	if session >= lim.SessionLossLimit {
		s.m.limitBlock.WithLabelValues(limitSession).Inc()
		return block(limitSession, fmt.Sprintf("Session loss %d has reached the limit of %d.", session, lim.SessionLossLimit),
			map[string]any{"loss_session": session, "max": lim.SessionLossLimit})
	}
	// 5. cooldown: too many losses inside the recent cooldown window
	if lim.CooldownLosses > 0 && lim.CooldownSeconds > 0 {
		since := now.Add(-time.Duration(lim.CooldownSeconds) * time.Second)
		recent, err := s.repo.LossCountSince(ctx, agentPublicID, since)
		if err != nil {
			return err
		}
		if recent >= lim.CooldownLosses {
			s.m.limitBlock.WithLabelValues(limitCooldown).Inc()
			return block(limitCooldown,
				fmt.Sprintf("In cooldown: %d losses in the last %ds (limit %d). Try again later.", recent, lim.CooldownSeconds, lim.CooldownLosses),
				map[string]any{"recent_losses": recent, "cooldown_seconds": lim.CooldownSeconds})
		}
	}
	// 6. active matches < max_concurrent_matches
	active, err := s.repo.ActiveMatchCount(ctx, agentPublicID)
	if err != nil {
		return err
	}
	if active >= lim.MaxConcurrentMatches {
		s.m.limitBlock.WithLabelValues(limitConcurrent).Inc()
		return block(limitConcurrent, fmt.Sprintf("Already in %d active matches (limit %d).", active, lim.MaxConcurrentMatches),
			map[string]any{"active": active, "max": lim.MaxConcurrentMatches})
	}
	// 7. bid ≤ max_bid
	if bid > lim.MaxBid {
		s.m.limitBlock.WithLabelValues(limitMaxBid).Inc()
		return block(limitMaxBid, fmt.Sprintf("Bid %d exceeds the maximum bid of %d.", bid, lim.MaxBid),
			map[string]any{"bid": bid, "max": lim.MaxBid})
	}
	return nil
}

// View is the `/v1/wallet` read model: balance, the configured limits, current
// usage, and the headroom that remains before each limit would block a join.
type View struct {
	Agent   string      `json:"agent"`
	Balance int64       `json:"balance"`
	Limits  AgentLimits `json:"limits"`
	Usage   Usage       `json:"usage"`
}

// Usage is the live limit utilisation for an agent.
type Usage struct {
	LossToday       int64 `json:"loss_today"`
	LossSession     int64 `json:"loss_session"`
	ActiveMatches   int   `json:"active_matches"`
	RecentLosses    int   `json:"recent_losses"` // within the cooldown window
	InCooldown      bool  `json:"in_cooldown"`
	DailyHeadroom   int64 `json:"daily_headroom"` // coins still loseable today
	SessionHeadroom int64 `json:"session_headroom"`
	ConcurrentFree  int   `json:"concurrent_free"` // additional matches joinable now
}

// View assembles the agent's wallet view (balance + limits + usage).
func (s *Service) View(ctx context.Context, agentPublicID string) (View, error) {
	lim, err := s.repo.AgentLimits(ctx, agentPublicID)
	if err != nil {
		return View{}, err
	}
	bal, err := s.ledger.Balance(ctx, agentPublicID)
	if err != nil {
		return View{}, err
	}
	now := s.clock.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	daily, err := s.repo.LossSince(ctx, agentPublicID, dayStart)
	if err != nil {
		return View{}, err
	}
	session, err := s.repo.LossSince(ctx, agentPublicID, now.Add(-s.cfg.SessionWindow))
	if err != nil {
		return View{}, err
	}
	active, err := s.repo.ActiveMatchCount(ctx, agentPublicID)
	if err != nil {
		return View{}, err
	}
	recent := 0
	if lim.CooldownLosses > 0 && lim.CooldownSeconds > 0 {
		recent, err = s.repo.LossCountSince(ctx, agentPublicID, now.Add(-time.Duration(lim.CooldownSeconds)*time.Second))
		if err != nil {
			return View{}, err
		}
	}
	return View{
		Agent:   agentPublicID,
		Balance: bal,
		Limits:  lim,
		Usage: Usage{
			LossToday:       daily,
			LossSession:     session,
			ActiveMatches:   active,
			RecentLosses:    recent,
			InCooldown:      lim.CooldownLosses > 0 && recent >= lim.CooldownLosses,
			DailyHeadroom:   nonNeg(lim.DailyLossLimit - daily),
			SessionHeadroom: nonNeg(lim.SessionLossLimit - session),
			ConcurrentFree:  maxInt(0, lim.MaxConcurrentMatches-active),
		},
	}, nil
}

// History returns the agent's ledger-backed transaction history.
func (s *Service) History(ctx context.Context, agentPublicID string, limit int) ([]HistoryLine, error) {
	lines, err := s.ledger.History(ctx, agentPublicID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryLine, len(lines))
	for i, l := range lines {
		out[i] = HistoryLine{TxnID: l.TxnPublicID, Kind: l.Kind, Amount: l.Amount, CreatedAt: l.CreatedAt}
	}
	return out, nil
}

// HistoryLine is the wire shape for one history row (decoupled from ledger.Line).
type HistoryLine struct {
	TxnID     string    `json:"txn_id"`
	Kind      string    `json:"kind"`
	Amount    int64     `json:"amount"`
	CreatedAt time.Time `json:"created_at"`
}

// OwnerOf exposes the agent→owner lookup for read authorization in the handler.
func (s *Service) OwnerOf(ctx context.Context, agentPublicID string) (string, error) {
	return s.repo.OwnerOf(ctx, agentPublicID)
}

func nonNeg(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
