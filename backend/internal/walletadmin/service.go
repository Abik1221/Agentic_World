package walletadmin

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/agent-arena/arena/internal/platform"
)

// Service serves the wallet-admin controls and the deposit/withdrawal gate. It
// caches settings in-process with a short TTL so the gate never hits the DB on
// every payment; mutations invalidate the cache.
type Service struct {
	repo  Repo
	adj   Adjuster
	clock platform.Clock
	log   *slog.Logger
	ttl   time.Duration

	mu      sync.RWMutex
	cache   *Settings
	cacheAt time.Time
}

// New builds the service. adj may be nil (manual adjustment then returns an error).
func New(repo Repo, adj Adjuster, clock platform.Clock, log *slog.Logger) *Service {
	return &Service{repo: repo, adj: adj, clock: clock, log: log, ttl: 10 * time.Second}
}

// Settings returns the current (possibly cached) settings.
func (s *Service) Settings(ctx context.Context) (Settings, error) { return s.settings(ctx) }

func (s *Service) settings(ctx context.Context) (Settings, error) {
	s.mu.RLock()
	if s.cache != nil && s.clock.Now().Sub(s.cacheAt) < s.ttl {
		c := *s.cache
		s.mu.RUnlock()
		return c, nil
	}
	s.mu.RUnlock()

	st, err := s.repo.GetSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	s.mu.Lock()
	s.cache, s.cacheAt = &st, s.clock.Now()
	s.mu.Unlock()
	return st, nil
}

func (s *Service) invalidate() {
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
}

// UpdateSettings validates and persists new settings, then returns the fresh row.
func (s *Service) UpdateSettings(ctx context.Context, actor string, st Settings) (Settings, error) {
	if st.MinDepositBase < 0 || st.MinWithdrawCoins < 0 || st.MaxWithdrawCoins < 0 {
		return Settings{}, errInvalid("amounts must be non-negative")
	}
	if st.WithdrawFeePct < 0 || st.WithdrawFeePct > 100 {
		return Settings{}, errInvalid("withdraw_fee_pct must be 0–100")
	}
	if st.Confirmations < 0 {
		return Settings{}, errInvalid("confirmations must be non-negative")
	}
	if st.MaxWithdrawCoins > 0 && st.MaxWithdrawCoins < st.MinWithdrawCoins {
		return Settings{}, errInvalid("max_withdraw_coins cannot be below min_withdraw_coins")
	}
	if err := s.repo.UpdateSettings(ctx, st); err != nil {
		return Settings{}, err
	}
	s.invalidate()
	s.audit(ctx, actor, "wallet_settings_update", "settings", st)
	return s.settings(ctx)
}

// SetFrozen freezes or unfreezes an owner's treasury wallet.
func (s *Service) SetFrozen(ctx context.Context, actor, userPublicID string, frozen bool) error {
	changed, err := s.repo.SetFrozen(ctx, userPublicID, frozen)
	if err != nil {
		return err
	}
	if !changed {
		return ErrUserNotFound
	}
	action := "wallet_freeze"
	if !frozen {
		action = "wallet_unfreeze"
	}
	s.audit(ctx, actor, action, userPublicID, nil)
	return nil
}

// Adjust applies a manual balance adjustment (coins > 0 credit, < 0 debit).
func (s *Service) Adjust(ctx context.Context, actor, userPublicID string, coins int64, reason string) error {
	if coins == 0 {
		return errInvalid("coins must be non-zero")
	}
	if s.adj == nil {
		return errInvalid("manual adjustment is not available")
	}
	idem := "adjust:" + platform.NewID(platform.PrefixTxn)
	if err := s.adj.AdminAdjust(ctx, userPublicID, coins, idem, reason); err != nil {
		return err
	}
	s.audit(ctx, actor, "wallet_adjust", userPublicID, map[string]any{"coins": coins, "reason": reason, "idem": idem})
	return nil
}

// CheckDeposit is the deposit gate: maintenance / enablement / minimum / freeze.
func (s *Service) CheckDeposit(ctx context.Context, userPublicID string, amountBase int64) error {
	st, err := s.settings(ctx)
	if err != nil {
		return err
	}
	if st.MaintenanceMode {
		return ErrMaintenance
	}
	if !st.DepositsEnabled {
		return ErrDepositsDisabled
	}
	if amountBase < st.MinDepositBase {
		return ErrBelowMin
	}
	frozen, err := s.repo.IsFrozen(ctx, userPublicID)
	if err != nil {
		return err
	}
	if frozen {
		return ErrFrozen
	}
	return nil
}

// CheckWithdraw is the withdrawal gate: maintenance / enablement / bounds / freeze.
func (s *Service) CheckWithdraw(ctx context.Context, userPublicID string, coins int64) error {
	st, err := s.settings(ctx)
	if err != nil {
		return err
	}
	if st.MaintenanceMode {
		return ErrMaintenance
	}
	if !st.WithdrawalsEnabled {
		return ErrWithdrawalsDisabled
	}
	if coins < st.MinWithdrawCoins {
		return ErrBelowMin
	}
	if st.MaxWithdrawCoins > 0 && coins > st.MaxWithdrawCoins {
		return ErrAboveMax
	}
	frozen, err := s.repo.IsFrozen(ctx, userPublicID)
	if err != nil {
		return err
	}
	if frozen {
		return ErrFrozen
	}
	return nil
}

func (s *Service) audit(ctx context.Context, actor, action, target string, detail any) {
	var b []byte
	if detail != nil {
		b, _ = json.Marshal(detail)
	}
	if err := s.repo.Audit(ctx, actor, action, target, b); err != nil {
		s.log.Error("walletadmin: audit write failed", "action", action, "target", target, "error", err)
	}
}
