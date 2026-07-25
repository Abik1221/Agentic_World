// Package payout implements cash-out: converting coins back to real money via a
// request → admin-approve → payout workflow. The user's FULL balance is withdrawable
// anytime (deposited coins included, not just winnings); the platform's margin is the
// fee taken on every deposit and every withdrawal, so a deposit→cash-out round-trip
// costs ~10% — which, with the anti-fraud gate, KYC/verified-wallet checks, velocity
// caps and the new-address cooldown, is what deters card-laundering (instead of locking
// deposits in play). Coins are HELD in escrow on request and only burned on a confirmed
// payout, so reconciliation stays exact.
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
	// Withdrawable is the coins the agent can cash out right now: current wallet
	// balance minus coins already committed to live withdrawals (full balance,
	// deposited coins included).
	Withdrawable(ctx context.Context, agentPublicID string) (int64, error)
	// AgentOwner returns the owning user's public id and Stripe Connect account id
	// ("" if not KYC-onboarded). ErrNotFound if the agent does not exist.
	AgentOwner(ctx context.Context, agentPublicID string) (ownerUserPublicID, connectAccountID string, err error)
	// DestinationWallet returns the owner's linked Solana wallet address ("" if
	// none) — the payout destination in Solana mode. ErrNotFound if the user is unknown.
	DestinationWallet(ctx context.Context, ownerUserPublicID string) (walletAddress string, err error)
	// VerifiedWallet returns the owner's ownership-PROVEN Solana wallet ("" if the
	// linked wallet's ownership was never verified via a signed challenge). Payouts
	// require it to equal the linked destination so funds only go to a proven wallet.
	VerifiedWallet(ctx context.Context, ownerUserPublicID string) (walletAddress string, err error)
	// VerifiedWalletAt returns the proven wallet AND when it was verified (zero time
	// if none). Drives the new-address cooldown: a freshly (re)verified wallet is
	// frozen for a window so a taken-over account can't swap the payout wallet and
	// immediately drain.
	VerifiedWalletAt(ctx context.Context, ownerUserPublicID string) (walletAddress string, verifiedAt time.Time, err error)
	// WithdrawnSince reports how many withdrawals an owner has filed since `since`
	// and their total net cents, counting all non-terminal-failed states (requested/
	// processing/broadcasted/paid) — the basis for the rolling velocity caps.
	WithdrawnSince(ctx context.Context, ownerUserPublicID string, since time.Time) (count int, netCents int64, err error)
	// AgentFlagged reports an active fraud flag (anti-fraud gate at request/approve).
	AgentFlagged(ctx context.Context, agentPublicID string) (bool, error)
	// OutstandingDebt is un-recovered chargeback debt; > 0 blocks withdrawals.
	OutstandingDebt(ctx context.Context, agentPublicID string) (int64, error)
	Create(ctx context.Context, w Withdrawal) error
	Get(ctx context.Context, publicID string) (Withdrawal, error)
	// GetByTransferID resolves the withdrawal a Stripe transfer belongs to, so a
	// transfer.reversed webhook can reconcile it. ErrNotFound if unknown.
	GetByTransferID(ctx context.Context, transferID string) (Withdrawal, error)
	// SetStatus transitions status from→to (idempotent); changed=false if the row
	// was not in `from`. Optionally records a transfer id / reason.
	SetStatus(ctx context.Context, publicID, from, to, transferID, reason string) (changed bool, err error)
	Audit(ctx context.Context, actor, action, target string, detail []byte) error
	ListByOwner(ctx context.Context, ownerUserPublicID string, limit int) ([]Withdrawal, error)
	ListByStatus(ctx context.Context, status string, limit int) ([]Withdrawal, error)
	// PendingByConnectAccount returns the still-requested withdrawals routed to a
	// connected account — used to auto-clear them once KYC completes.
	PendingByConnectAccount(ctx context.Context, connectAccountID string) ([]Withdrawal, error)
	// WithOwnerLock runs fn while holding an exclusive per-owner lock, so the
	// entitlement read (Withdrawable/WithdrawnSince) and the Create it guards are
	// atomic against a sibling request for the SAME owner. Without it two concurrent
	// requests each read stale state (before either row exists) and both pass,
	// letting an owner withdraw past their net winnings and blow the velocity caps.
	// Implemented with a Postgres session advisory lock keyed on the owner id.
	WithOwnerLock(ctx context.Context, ownerUserPublicID string, fn func() error) error
}

// Bank moves coins through the ledger. Satisfied by an adapter over ledger.Service.
// All are idempotent on the withdrawal id.
type Bank interface {
	Hold(ctx context.Context, withdrawalID, agentPublicID string, coins int64) error    // agent → escrow
	Release(ctx context.Context, withdrawalID, agentPublicID string, coins int64) error // escrow → agent (reject/fail)
	// Payout burns held coins: escrow → platform_revenue (fee) + stripe_clearing (rest).
	Payout(ctx context.Context, withdrawalID, agentPublicID string, coins, feeCoins int64) error
	// ReversePayout is the exact inverse of Payout: the money came back (Stripe
	// reversed the transfer), so re-credit the agent's coins and undo the fee/
	// clearing postings. Idempotent on the withdrawal id.
	ReversePayout(ctx context.Context, withdrawalID, agentPublicID string, coins, feeCoins int64) error
}

// Payout rails. Chain is stored per-withdrawal so behaviour is fixed at request
// time even if the platform later switches rails.
const (
	ChainStripe = "stripe" // Stripe Connect payout (destination = connect account)
	ChainSolana = "solana" // Solana USDC transfer (destination = wallet address)
)

// Transferrer sends money to a destination. In Stripe mode the destination is a
// connected account; in Solana mode it is a wallet address. DevTransferrer runs
// offline; StripeTransferrer / SolanaTransferrer hit the real APIs. The transfer
// MUST be idempotent per idemKey where the rail supports it (Stripe); Solana
// double-broadcast is prevented by the service's 'processing' claim.
type Transferrer interface {
	Transfer(ctx context.Context, destination string, amountCents int64, idemKey string) (transferID string, err error)
	// PayoutsEnabled reports whether the destination can actually receive the
	// payout (Stripe: KYC/`payouts_enabled`; Solana: a valid wallet address).
	// Guards Approve so we never attempt a payout to an unusable destination.
	PayoutsEnabled(ctx context.Context, destination string) (bool, error)
}

// Gate is the Super Admin withdrawal gate (satisfied by walletadmin.Service): it
// blocks a request on maintenance mode, a disabled withdrawal switch, out-of-bounds
// amounts, or a frozen wallet. Optional (nil ⇒ no dynamic gate).
type Gate interface {
	CheckWithdraw(ctx context.Context, userPublicID string, coins int64) error
}

// Notifier writes a user notification (idempotent per kind+ref). Optional.
type Notifier interface {
	Notify(ctx context.Context, userPublicID, kind, ref string, payload []byte) error
}

// Confirmer reports the terminal on-chain state of a broadcast transaction. Only
// used in Solana mode (satisfied by SolanaTransferrer); nil in Stripe mode.
type Confirmer interface {
	// Confirm returns finalized=false while the tx is still pending. Once
	// finalized: success=true ⇒ confirmed OK (burn escrow), success=false ⇒ the
	// transaction failed on-chain (release escrow).
	Confirm(ctx context.Context, signature string) (finalized bool, success bool, err error)
}

// Withdrawal is a cash-out record.
type Withdrawal struct {
	PublicID       string    `json:"withdrawal_id"`
	Agent          string    `json:"agent"`
	Owner          string    `json:"-"`
	Coins          int64     `json:"coins"`
	FeeCoins       int64     `json:"fee_coins"`
	GrossCents     int64     `json:"gross_cents"`
	StripeFeeCents int64     `json:"stripe_fee_cents"`
	NetCents       int64     `json:"net_cents"`
	ConnectAccount string    `json:"-"`
	Chain          string    `json:"chain"`
	DestWallet     string    `json:"dest_wallet,omitempty"` // Solana payout destination
	Status         string    `json:"status"`
	TransferID     string    `json:"transfer_id,omitempty"`
	RequestedAt    time.Time `json:"requested_at"`
	// StatusChangedAt is when the row last changed status (DB resolved_at, which
	// SetStatus stamps on every transition). Populated by ListByStatus; drives the
	// stuck-'processing' recovery sweep. Zero when not selected.
	StatusChangedAt time.Time `json:"-"`
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
