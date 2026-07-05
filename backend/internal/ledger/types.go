// Package ledger is the double-entry money core: the single place a coin ever
// moves. Every balance change is a balanced Txn (Σ postings == 0) applied
// atomically and at most once per idempotency key. Higher layers (wallet) build
// domain transactions (stake/settle/refund/topup) and hand them here; nothing
// else may write to wallets. See docs/architecture/data-model.md (ledger invariants).
package ledger

import "time"

// System wallet kinds (counter-accounts that are not owned by an agent).
const (
	SysEscrow          = "escrow"           // holds live match stakes
	SysPlatformRevenue = "platform_revenue" // accrues rake
	SysStripeClearing  = "stripe_clearing"  // money in/out (real in Stage 5; mint source in non-prod)
	SysBadDebt         = "bad_debt"          // un-recovered chargeback receivable (may go negative)
)

// Transaction kinds.
const (
	KindTopup    = "topup"    // real money (or test mint) in → user wallet
	KindAllocate = "allocate" // user wallet → agent wallet (owner funding)
	KindStake    = "stake"    // agent → escrow at match start
	KindSettle   = "settle"   // escrow → winner (+rake) / tie split at match end
	KindRefund   = "refund"   // escrow → players on abort
	KindReversal = "reversal" // claw back a top-up on a Stripe refund/chargeback
)

// WalletRef names a wallet by agent, user, or system kind. Exactly one field is set.
type WalletRef struct {
	Agent  string // agent public id
	User   string // owner user public id (treasury wallet)
	System string // system wallet kind
}

// AgentWallet refers to an agent's wallet by public id.
func AgentWallet(agentPublicID string) WalletRef { return WalletRef{Agent: agentPublicID} }

// UserWallet refers to an owner's treasury wallet by user public id.
func UserWallet(userPublicID string) WalletRef { return WalletRef{User: userPublicID} }

// SystemWallet refers to a system counter-account by kind.
func SystemWallet(kind string) WalletRef { return WalletRef{System: kind} }

// IsSystem reports whether the ref targets a system wallet.
func (w WalletRef) IsSystem() bool { return w.System != "" }

// IsUser reports whether the ref targets an owner's treasury wallet.
func (w WalletRef) IsUser() bool { return w.User != "" }

// Posting is one signed leg of a transaction: +credit, -debit.
type Posting struct {
	Wallet WalletRef
	Amount int64
}

// Txn is a balanced set of postings applied as one unit, deduplicated by Key.
type Txn struct {
	Kind     string
	Key      string // idempotency key, globally unique (e.g. "settle:m_777")
	Metadata map[string]any
	Postings []Posting
}

// Line is one agent-facing ledger row (a single entry joined to its txn).
type Line struct {
	TxnPublicID string    `json:"txn_id"`
	Kind        string    `json:"kind"`
	Amount      int64     `json:"amount"`
	CreatedAt   time.Time `json:"created_at"`
}

// Drift is a wallet whose cached balance disagrees with the sum of its entries.
type Drift struct {
	WalletID int64  `json:"wallet_id"`
	Kind     string `json:"kind"`
	Balance  int64  `json:"balance"`
	EntrySum int64  `json:"entry_sum"`
}
