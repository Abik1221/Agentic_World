package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// The operator's money view of ONE developer.
//
// UserDetail already carried lifetime totals, but an operator answering "this
// person says their withdrawal never arrived" needs a different set of facts:
// which wallets are attached to the account, which of them is actually
// PROVEN — only a wallet whose ownership was signed for can receive a payout —
// where the balance is sitting right now, and the ledger lines behind it.
// Without those, every such ticket became a request to someone with database
// access, and the answer arrived as a screenshot.

// ConnectedWallet is one address associated with the account.
type ConnectedWallet struct {
	Address string `json:"address"`
	// Provider is the wallet brand captured at login (phantom, solflare, privy…).
	Provider string `json:"provider,omitempty"`
	// Role is "login_hint" or "payout_destination".
	//
	// The distinction is the whole point of showing both. The login hint is
	// UNVERIFIED — it is whatever the auth provider reported and it can be wrong or
	// injected. Only the payout destination has been proven by a signed challenge,
	// and only it can receive money. An operator who confuses the two can talk a
	// user into expecting a payout to an address that will never be paid.
	Role string `json:"role"`
	// Verified is true only for an address whose ownership was proven by signature.
	Verified bool `json:"verified"`
	// VerifiedAt is when that proof was accepted. Also drives the new-address
	// cooldown, so an operator can see why a payout is being held.
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
}

// AgentBalance is one of the user's agents and the coins allocated to it.
type AgentBalance struct {
	Agent            string `json:"agent"`
	Name             string `json:"name,omitempty"`
	Balance          int64  `json:"balance"`
	LockedInMatches  int64  `json:"locked_in_matches"`
	PendingWithdrawn int64  `json:"pending_withdrawals"`
	ActiveMatches    int    `json:"active_matches"`
}

// UserWalletDetail is the full money picture for one account.
type UserWalletDetail struct {
	User string `json:"user"`
	// Frozen is the Super Admin wallet freeze. Surfaced here because a frozen
	// wallet is the most common reason a payment "silently" does nothing, and it
	// is invisible from every other screen.
	Frozen bool `json:"frozen"`

	Wallets []ConnectedWallet `json:"wallets"`

	// Balances, all in coins. Treasury is the owner's own pot; the agent figures
	// are money already pushed out to agents and are NOT part of it.
	TreasuryBalance int64 `json:"treasury_balance"`
	AgentsBalance   int64 `json:"agents_balance"`
	LockedInMatches int64 `json:"locked_in_matches"`
	PendingPayouts  int64 `json:"pending_payouts"`
	// Total is every coin the account controls, wherever it sits. Stated once so
	// an operator does not add up four figures by hand and get it wrong.
	Total int64 `json:"total"`
	// CoinCents prices all of the above.
	CoinCents int64 `json:"coin_cents"`

	Agents []AgentBalance `json:"agents"`
}

// LedgerLine is one entry against the user's own treasury wallet.
//
// No running balance: ledger_entries does not store one, and deriving it per row
// would be a guess that silently disagrees with wallets.balance the moment a page
// boundary or a concurrent write intervenes. The authoritative balance is served
// once, by UserWalletDetail.
type LedgerLine struct {
	TxnID     string         `json:"txn_id"`
	Kind      string         `json:"kind"`
	Amount    int64          `json:"amount"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// WalletRepo is the extra read access this surface needs. Kept separate from Repo
// so an implementation can adopt it independently.
type WalletRepo interface {
	UserWallet(ctx context.Context, userPublicID string) (UserWalletDetail, bool, error)
	UserTransactions(ctx context.Context, userPublicID string, limit, offset int) ([]LedgerLine, error)
}

// SetWalletRepo wires the per-user money reads. Optional: without it the two
// routes below answer 503 rather than 404, because "this feature is not wired"
// and "this user has no wallet" must not look the same to an operator.
func (h *Handler) SetWalletRepo(w WalletRepo) { h.wallets = w }

// RegisterWallet mounts the per-user money routes. Called from Register.
func (h *Handler) registerWallet(r chi.Router, guard func(http.Handler) http.Handler) {
	r.With(guard).Get("/v1/admin/users/{id}/wallet", h.userWallet)
	r.With(guard).Get("/v1/admin/users/{id}/transactions", h.userTransactions)
}

func (h *Handler) userWallet(w http.ResponseWriter, r *http.Request) {
	if h.wallets == nil {
		httpx.Error(w, httpx.NewError(http.StatusServiceUnavailable, "wallet_reads_unconfigured",
			"Per-user wallet reads are not configured on this deployment."))
		return
	}
	d, found, err := h.wallets.UserWallet(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if !found {
		httpx.Error(w, httpx.ErrNotFound)
		return
	}
	d.CoinCents = h.coinCents
	// Never cached. An operator acting on a stale balance is exactly how a double
	// refund happens — the same rule userDetail already follows.
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, d)
}

func (h *Handler) userTransactions(w http.ResponseWriter, r *http.Request) {
	if h.wallets == nil {
		httpx.Error(w, httpx.NewError(http.StatusServiceUnavailable, "wallet_reads_unconfigured",
			"Per-user wallet reads are not configured on this deployment."))
		return
	}
	limit, offset := page(r)
	items, err := h.wallets.UserTransactions(r.Context(), chi.URLParam(r, "id"), limit, offset)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if items == nil {
		items = []LedgerLine{}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"transactions": items, "limit": limit, "offset": offset, "coin_cents": h.coinCents,
	})
}
