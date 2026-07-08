package store

import (
	"context"
	"errors"

	"github.com/agent-arena/arena/internal/walletadmin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WalletAdminRepo is the pgx implementation of walletadmin.Repo.
type WalletAdminRepo struct{ db *pgxpool.Pool }

func NewWalletAdminRepo(db *pgxpool.Pool) *WalletAdminRepo { return &WalletAdminRepo{db: db} }

var _ walletadmin.Repo = (*WalletAdminRepo)(nil)

func (r *WalletAdminRepo) GetSettings(ctx context.Context) (walletadmin.Settings, error) {
	var s walletadmin.Settings
	err := r.db.QueryRow(ctx,
		`SELECT deposits_enabled, withdrawals_enabled, maintenance_mode, min_deposit_base,
		        min_withdraw_coins, max_withdraw_coins, withdraw_fee_pct, confirmations, updated_at
		 FROM wallet_settings WHERE id = 1`).
		Scan(&s.DepositsEnabled, &s.WithdrawalsEnabled, &s.MaintenanceMode, &s.MinDepositBase,
			&s.MinWithdrawCoins, &s.MaxWithdrawCoins, &s.WithdrawFeePct, &s.Confirmations, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Singleton missing (migration seeds it): fall back to safe defaults.
		return walletadmin.Settings{
			DepositsEnabled: true, WithdrawalsEnabled: true,
			MinDepositBase: 1_000_000, MinWithdrawCoins: 500, WithdrawFeePct: 10, Confirmations: 1,
		}, nil
	}
	return s, err
}

func (r *WalletAdminRepo) UpdateSettings(ctx context.Context, s walletadmin.Settings) error {
	_, err := r.db.Exec(ctx,
		`UPDATE wallet_settings SET
		     deposits_enabled = $1, withdrawals_enabled = $2, maintenance_mode = $3,
		     min_deposit_base = $4, min_withdraw_coins = $5, max_withdraw_coins = $6,
		     withdraw_fee_pct = $7, confirmations = $8, updated_at = now()
		 WHERE id = 1`,
		s.DepositsEnabled, s.WithdrawalsEnabled, s.MaintenanceMode, s.MinDepositBase,
		s.MinWithdrawCoins, s.MaxWithdrawCoins, s.WithdrawFeePct, s.Confirmations)
	return err
}

func (r *WalletAdminRepo) SetFrozen(ctx context.Context, userPublicID string, frozen bool) (bool, error) {
	ct, err := r.db.Exec(ctx,
		`UPDATE wallets SET frozen = $2
		 WHERE user_id = (SELECT id FROM users WHERE public_id = $1)`,
		userPublicID, frozen)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}

func (r *WalletAdminRepo) IsFrozen(ctx context.Context, userPublicID string) (bool, error) {
	var frozen bool
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(w.frozen, false)
		 FROM wallets w JOIN users u ON u.id = w.user_id
		 WHERE u.public_id = $1`, userPublicID).Scan(&frozen)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // no treasury wallet ⇒ not frozen
	}
	return frozen, err
}

func (r *WalletAdminRepo) Audit(ctx context.Context, actor, action, target string, detail []byte) error {
	d := string(detail)
	if d == "" {
		d = "{}"
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO audit_log (actor, action, target, detail) VALUES ($1, $2, $3, $4::jsonb)`,
		actor, action, nullString(target), d)
	return err
}
