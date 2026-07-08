// Package walletadmin is the Super Admin control surface for the wallet system
// (Beta wallet pipeline P4): runtime settings (deposit/withdrawal switches,
// maintenance mode, min/max bounds, fee, confirmations) and risk actions (freeze
// / unfreeze a wallet, manual balance adjustment). The deposit and withdrawal
// services consult this as a nil-safe Gate, so payments can be paused or a wallet
// frozen without a redeploy. Every route is Platform-or-admin authorized (same as
// adminapi); every mutation is audited.
package walletadmin

import (
	"context"
	"time"
)

// Settings is the single-row runtime configuration the Super Admin controls.
type Settings struct {
	DepositsEnabled    bool      `json:"deposits_enabled"`
	WithdrawalsEnabled bool      `json:"withdrawals_enabled"`
	MaintenanceMode    bool      `json:"maintenance_mode"`
	MinDepositBase     int64     `json:"min_deposit_base"`   // USDC base units (6 decimals)
	MinWithdrawCoins   int64     `json:"min_withdraw_coins"` // coins
	MaxWithdrawCoins   int64     `json:"max_withdraw_coins"` // coins; 0 = no cap
	WithdrawFeePct     int       `json:"withdraw_fee_pct"`
	Confirmations      int       `json:"confirmations"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// Repo is the wallet-admin persistence port (pgx impl in internal/store).
type Repo interface {
	GetSettings(ctx context.Context) (Settings, error)
	UpdateSettings(ctx context.Context, s Settings) error
	// SetFrozen sets the owner's treasury freeze flag; changed=false if no such
	// wallet exists.
	SetFrozen(ctx context.Context, userPublicID string, frozen bool) (changed bool, err error)
	IsFrozen(ctx context.Context, userPublicID string) (bool, error)
	Audit(ctx context.Context, actor, action, target string, detail []byte) error
}

// Adjuster applies a manual balance adjustment (satisfied by wallet.Service).
type Adjuster interface {
	AdminAdjust(ctx context.Context, userPublicID string, coins int64, idemKey, reason string) error
}
