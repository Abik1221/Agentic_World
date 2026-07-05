// Package payout implements cash-out: converting coins back to real money via a
// request → admin-approve → Stripe-payout workflow. Only NET WINNINGS are
// withdrawable (deposited/bonus coins are play-only), which removes the
// buy→withdraw arbitrage and card-laundering vectors. Coins are HELD in escrow on
// request and only burned on a confirmed payout, so reconciliation stays exact.
//
// Economic model (1 coin = CoinCents): the platform takes a sell fee and the
// Stripe payout fee is passed to the user — both deducted from the gross. The
// player only ever sees coins; the cut is invisible. See docs/coin-engine-critique.md.
package payout

import (
	"context"
	"time"
)

// Repo is the cash-out persistence + read port.
type Repo interface {
	// Withdrawable is net play winnings still available (lifetime match P/L minus
	// coins already committed to live withdrawals), capped at the current balance.
	Withdrawable(ctx context.Context, agentPublicID string) (int64, error)
	// AgentOwner returns the owning user's public id and Stripe Connect account id
	// ("" if not KYC-onboarded). ErrNotFound if the agent does not exist.
	AgentOwner(ctx context.Context, agentPublicID string) (ownerUserPublicID, connectAccountID string, err error)
	// AgentFlagged reports an active fraud flag (anti-fraud gate at request/approve).
	AgentFlagged(ctx context.Context, agentPublicID string) (bool, error)
	// OutstandingDebt is un-recovered chargeback debt; > 0 blocks withdrawals.
	OutstandingDebt(ctx context.Context, agentPublicID string) (int64, error)
	Create(ctx context.Context, w Withdrawal) error
	Get(ctx context.Context, publicID string) (Withdrawal, error)
	// SetStatus transitions status from→to (idempotent); changed=false if the row
	// was not in `from`. Optionally records a transfer id / reason.
	SetStatus(ctx context.Context, publicID, from, to, transferID, reason string) (changed bool, err error)
	Audit(ctx context.Context, actor, action, target string, detail []byte) error
	ListByOwner(ctx context.Context, ownerUserPublicID string, limit int) ([]Withdrawal, error)
	ListByStatus(ctx context.Context, status string, limit int) ([]Withdrawal, error)
}

// Bank moves coins through the ledger. Satisfied by an adapter over ledger.Service.
// All three are idempotent on the withdrawal id.
type Bank interface {
	Hold(ctx context.Context, withdrawalID, agentPublicID string, coins int64) error    // agent → escrow
	Release(ctx context.Context, withdrawalID, agentPublicID string, coins int64) error // escrow → agent (reject/fail)
	// Payout burns held coins: escrow → platform_revenue (fee) + stripe_clearing (rest).
	Payout(ctx context.Context, withdrawalID, agentPublicID string, coins, feeCoins int64) error
}

// Transferrer sends money to a connected account. DevTransferrer runs offline;
// StripeTransferrer hits the real API. Idempotent on idemKey.
type Transferrer interface {
	Transfer(ctx context.Context, connectAccountID string, amountCents int64, idemKey string) (transferID string, err error)
}

// Withdrawal is a cash-out record.
type Withdrawal struct {
	PublicID       string `json:"withdrawal_id"`
	Agent          string `json:"agent"`
	Owner          string `json:"-"`
	Coins          int64  `json:"coins"`
	FeeCoins       int64  `json:"fee_coins"`
	GrossCents     int64  `json:"gross_cents"`
	StripeFeeCents int64  `json:"stripe_fee_cents"`
	NetCents       int64  `json:"net_cents"`
	ConnectAccount string    `json:"-"`
	Status         string    `json:"status"`
	TransferID     string    `json:"transfer_id,omitempty"`
	RequestedAt    time.Time `json:"requested_at"`
}

// AdminWithdrawal adds operator context for the approval queue.
type AdminWithdrawal struct {
	Withdrawal
	Owner          string `json:"owner"`
	AgentName      string `json:"agent_name"`
	CanApprove     bool   `json:"can_approve"`
	ClearingWaitMs int64  `json:"clearing_wait_ms"`
}

// Quote is the fee breakdown for a prospective withdrawal (shown before confirm).
type Quote struct {
	Coins          int64 `json:"coins"`
	GrossCents     int64 `json:"gross_cents"`
	FeeCoins       int64 `json:"platform_fee_coins"`
	StripeFeeCents int64 `json:"stripe_fee_cents"`
	NetCents       int64 `json:"net_cents"`
}
