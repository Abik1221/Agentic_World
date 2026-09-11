// Package adminapi exposes the read-only admin surface the Super Admin backend
// consumes to build (and backfill) its live mirror: paginated lists of users,
// agents, matches, payments (topups) and disputes, plus a single revenue/overview
// aggregate. Every route is authorized by RequirePlatformOrAdmin — an Ed25519
// Platform service token OR a user in the ADMIN_USER_IDS allowlist — so it is
// purely additive to the existing surface and never widens agent/user scope.
//
// The package defines its own Repo interface (implemented by store.AdminRepo) so
// it never imports store, keeping the dependency arrow pointing inward.
package adminapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// maxPageSize caps a single admin read so a mirror backfill can page but never
// pull an unbounded result set in one request.
const (
	defaultPageSize = 50
	maxPageSize     = 500
)

// User is an admin-facing account row.
type User struct {
	PublicID  string    `json:"public_id"`
	XHandle   string    `json:"x_handle,omitempty"`
	Email     string    `json:"email,omitempty"`
	Status    string    `json:"status"`
	Agents    int       `json:"agents"`
	CreatedAt time.Time `json:"created_at"`
}

// Agent is an admin-facing agent row (with owner + current wallet balance).
type Agent struct {
	PublicID          string    `json:"public_id"`
	Name              string    `json:"name"`
	OwnerPublicID     string    `json:"owner_public_id"`
	Framework         string    `json:"framework,omitempty"`
	Status            string    `json:"status"`
	VerificationLevel string    `json:"verification_level"`
	Balance           int64     `json:"balance"`
	CreatedAt         time.Time `json:"created_at"`
}

// Match is an admin-facing match row.
type Match struct {
	PublicID    string     `json:"public_id"`
	Game        string     `json:"game"`
	Status string `json:"status"`
	// Mode decides whether a finished match settles at all: finalize skips the whole
	// settlement block for a sandbox table. A match carrying a BID but sitting in
	// sandbox mode therefore takes both stakes and never pays anyone, which is
	// invisible from every other admin read — the row looks like a normal finished
	// match. Surfaced here so that state can be seen rather than inferred.
	Mode        string     `json:"mode"`
	Bid         int64      `json:"bid"`
	WinnerAgent string     `json:"winner_agent,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// Payment is a coin topup (a ledger transaction of kind 'topup').
type Payment struct {
	PublicID  string    `json:"public_id"`
	Kind      string    `json:"kind"`
	Amount    int64     `json:"amount"`
	Agent     string    `json:"agent,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Dispute is an admin-facing dispute row.
type Dispute struct {
	PublicID   string     `json:"public_id"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	Match      string     `json:"match,omitempty"`
	Agent      string     `json:"agent,omitempty"`
	Reporter   string     `json:"reporter,omitempty"`
	Detail     string     `json:"detail,omitempty"`
	Resolution string     `json:"resolution,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// Overview is the single-round-trip platform aggregate for the admin dashboard.
type Overview struct {
	Users              int64     `json:"users"`
	Agents             int64     `json:"agents"`
	ActiveMatches      int64     `json:"active_matches"`
	FinishedMatches    int64     `json:"finished_matches"`
	OpenDisputes       int64     `json:"open_disputes"`
	PendingWithdrawals int64     `json:"pending_withdrawals"`
	PlatformRevenue    int64     `json:"platform_revenue_coins"`
	TopupVolume        int64     `json:"topup_volume_coins"`
	GeneratedAt        time.Time `json:"generated_at"`

	// CoinCents is the face value of one coin, so a consumer can price the coin
	// figures above in dollars without hardcoding the peg and silently drifting from
	// it. Everything below is in CENTS.
	CoinCents int64 `json:"coin_cents"`

	// The REAL on-chain treasury, from the solvency monitor's last reconciliation.
	//
	// These are not a currency conversion of the coin figures above, and must not be
	// presented as one. Coins valued in USD are what the platform has earned in its
	// own ledger; this is what is actually in the hot wallet. USDC is dollar-pegged,
	// so showing the same coin total twice under "USD" and "USDC" labels would be
	// theatre — the useful second number is this one.
	//
	// TreasuryObservedAt is zero when no reconciliation has landed yet. A consumer
	// MUST render that as "unknown", never as a zero balance: the difference between
	// "we hold nothing" and "we have not looked" is the difference between an
	// incident and a cold start.
	TreasuryUSDCCents  int64     `json:"treasury_usdc_cents"`
	LiabilityUSDCCents int64     `json:"liability_usdc_cents"`
	TreasuryObservedAt time.Time `json:"treasury_observed_at,omitempty"`

	// Custody breakdown. TreasuryUSDCCents above is the TOTAL the platform holds; these
	// say where it is sitting and whether anything needs a human to move it.
	//
	// The distinction that matters: HotUSDCCents is what can be paid out right now
	// without anyone touching a hardware wallet. When it is below the liability but the
	// total is not, the platform is solvent and the payout float simply needs a top-up —
	// a routine treasury task, not an incident. Rendering that as a shortfall would send
	// an operator hunting a theft that never happened, and on a split-custody deployment
	// it is the normal resting state.
	HotUSDCCents   int64 `json:"hot_usdc_cents"`
	VaultUSDCCents int64 `json:"vault_usdc_cents"`
	CustodySplit   bool  `json:"custody_split"`
	// SweepNeededCents is how far the hot wallet sits above its exposure ceiling, i.e.
	// how much ought to be moved to cold storage. 0 when within the cap or uncapped.
	SweepNeededCents  int64 `json:"sweep_needed_cents"`
	HotWalletCapCents int64 `json:"hot_wallet_cap_cents"`
	// ColdWalletAddress is where a sweep should go — surfaced so the operator does not
	// have to find it elsewhere at the moment they act on the alert.
	ColdWalletAddress string `json:"cold_wallet_address,omitempty"`
	// HotWalletSOL is the fee fuel. Payouts are Solana transactions and the hot wallet
	// pays their fees, so at zero every cash-out fails to broadcast while every figure
	// above still looks healthy. Absent when unchecked, which a consumer must render as
	// "not monitored" rather than as empty.
	HotWalletSOL    float64 `json:"hot_wallet_sol,omitempty"`
	HotWalletSOLMin float64 `json:"hot_wallet_sol_min,omitempty"`
	HotWalletSOLLow bool    `json:"hot_wallet_sol_low"`
	// PayoutsFundedOK is the single question an operator approving a cash-out wants
	// answered: can the wallet settle right now? False means approvals will be refused
	// until the float or the SOL is topped up.
	PayoutsFundedOK      bool   `json:"payouts_funded_ok"`
	PayoutsBlockedReason string `json:"payouts_blocked_reason,omitempty"`
}

// UserDetail is everything an operator needs about ONE developer, in one place.
//
// Deliberately a DIFFERENT shape from the public profile. A developer sees their
// record; an operator needs the money and the behaviour behind it — what was staked,
// what was paid out, what the platform earned from them, and whether any of it looks
// wrong. Reusing the public profile here would mean an operator investigating a
// refund had to open four screens and join the numbers by eye.
//
// Sandbox is reported SEPARATELY from competitive throughout, for the same reason it
// is on the public profile: practice against deterministic bots is not comparable to
// staked play, and a combined figure hides exactly the pattern an operator is looking
// for (someone farming practice, or someone whose real losses do not match their
// deposits).
type UserDetail struct {
	PublicID  string    `json:"public_id"`
	Username  string    `json:"username,omitempty"`
	Email     string    `json:"email,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`

	Agents int `json:"agents"`

	// Play, split by mode — never summed.
	CompetitiveMatches int `json:"competitive_matches"`
	SandboxMatches     int `json:"sandbox_matches"`
	Wins               int `json:"wins"`
	Losses             int `json:"losses"`

	// Money, all in COINS unless the name says cents. Balance is what they hold now;
	// the rest is lifetime flow.
	Balance          int64 `json:"balance"`
	LifetimeDeposits int64 `json:"lifetime_deposits"`
	LifetimeWinnings int64 `json:"lifetime_winnings"`
	WithdrawnCoins   int64 `json:"withdrawn_coins"`
	// PlatformRevenue is what the platform earned FROM THIS USER — rake on their
	// settled matches plus withdrawal fees. The number that answers "is this account
	// worth the support cost".
	PlatformRevenue int64 `json:"platform_revenue"`
	// PendingWithdrawals is money committed to leaving but not yet gone.
	PendingWithdrawals int64 `json:"pending_withdrawals"`

	// Tokens burned by their agents — the platform's inference cost for this user.
	TokensUsed int64 `json:"tokens_used"`
}

// Repo is the read-only data access the admin surface needs. Implemented by
// store.AdminRepo. All lists are newest-first and bounded by (limit, offset).
type Repo interface {
	ListUsers(ctx context.Context, limit, offset int) ([]User, error)
	ListAgents(ctx context.Context, limit, offset int) ([]Agent, error)
	ListMatches(ctx context.Context, status string, limit, offset int) ([]Match, error)
	ListPayments(ctx context.Context, limit, offset int) ([]Payment, error)
	ListDisputes(ctx context.Context, status string, limit, offset int) ([]Dispute, error)
	Overview(ctx context.Context) (Overview, error)
	// UserDetail is the per-developer operator view. Not the public profile.
	UserDetail(ctx context.Context, userPublicID string) (UserDetail, bool, error)
}

// Handler serves the admin-read routes.
type Handler struct {
	repo   Repo
	authn  *auth.Authenticator
	admins map[string]bool
	// coinCents prices the ledger's coin figures in dollars for the dashboard.
	coinCents int64
	// treasury reads the last on-chain reconciliation. Nil when payouts are not
	// configured, in which case the overview reports no treasury observation rather
	// than a zero balance.
	treasury TreasuryReader
	// wallets serves the per-user money view (connected wallets, balances, ledger
	// lines). Nil ⇒ those routes answer 503, never a misleading empty record.
	wallets WalletRepo
	// agentsRepo serves the per-user AGENT view: each agent's guardrails and how close it
	// is to hitting them. Nil ⇒ that route answers 503 for the same reason.
	agentsRepo AgentsRepo
	// queueHealth answers "where do agents get stuck in matchmaking". Nil ⇒ those routes
	// answer 503, for the same reason as the two above: an empty funnel would be read as a
	// healthy queue, which is the opposite of what an unwired recorder means.
	queueHealth QueueHealthReader
}

// TreasuryReader exposes the solvency monitor's most recent reconciliation.
// Satisfied by *payout.SolvencyMonitor; kept as an interface so adminapi does not
// depend on the payout package.
type TreasuryReader interface {
	LastReading() (balanceCents, liabilityCents int64, observedAt time.Time, ok bool)
}

// TreasuryDetailReader is the custody breakdown behind TreasuryReader: where the money
// is sitting, whether it needs moving, and whether a cash-out can be settled right now.
// Also satisfied by *payout.SolvencyMonitor, and also kept flat so this package stays
// independent of payout.
//
// OPTIONAL. A TreasuryReader that does not implement it leaves the breakdown fields at
// their zero values, which is exactly what a deployment with no Solana rail should
// report — there is no hot wallet to describe.
type TreasuryDetailReader interface {
	CustodyBreakdown() (hotCents, vaultCents int64, split, ok bool)
	FeeFuel() (lamports, minLamports int64, known bool)
	SweepNeeded() (excessCents, capCents int64, coldAddress string)
	CanPay(amountCents int64) (ok bool, reason string)
}

// lamportsPerSOL converts the fee-fuel figures for display. The API reports SOL because
// that is the unit an operator funding a wallet actually works in; the monitor keeps
// lamports internally so no threshold depends on float rounding.
const lamportsPerSOL = 1_000_000_000

// SetCoinCents wires the coin→USD peg used to price the dashboard's coin totals.
func (h *Handler) SetCoinCents(cents int64) {
	if cents > 0 {
		h.coinCents = cents
	}
}

// SetTreasury wires the on-chain USDC reader. Optional.
func (h *Handler) SetTreasury(t TreasuryReader) { h.treasury = t }

// NewHandler builds the admin-read handler. adminUserIDs are the user public ids
// (ADMIN_USER_IDS) allowed alongside any valid Platform token.
func NewHandler(repo Repo, authn *auth.Authenticator, adminUserIDs []string) *Handler {
	admins := make(map[string]bool, len(adminUserIDs))
	for _, id := range adminUserIDs {
		admins[id] = true
	}
	return &Handler{repo: repo, authn: authn, admins: admins, coinCents: 1}
}

// Register mounts the read-only admin routes behind Platform-or-admin auth.
func (h *Handler) Register(r chi.Router) {
	guard := auth.RequirePlatformOrAdmin(h.admins)
	r.Group(func(r chi.Router) {
		// ONE authn.Middleware. This line appeared twice — once here and once again
		// after the /v1/admin/users route below — so every admin route registered after
		// that second call authenticated twice per request, while /v1/admin/users
		// authenticated once.
		//
		// It did not panic, which is why it survived: chi's "all middlewares must be
		// defined before routes" guard checks mx.handler, and inside a Group the router
		// is an INLINE mux whose handler is not set by a `With(...).Get(...)` route — that
		// sets the handler on the new inline mux returned by With, not on this one. So the
		// second Use was accepted silently.
		//
		// The visible symptom was in the headers: denySharedCaching runs on each pass and
		// uses Header().Add, so those routes answered with
		// `Vary: Authorization, Cookie, Authorization, Cookie`. It also verified the Super
		// Admin's Ed25519 Platform token twice on every admin request. Harmless, but it
		// left the group in a state where reordering these lines WOULD hit the real panic
		// at boot.
		r.Use(h.authn.Middleware)
		r.With(guard).Get("/v1/admin/queue-health", h.queueFunnel)
		r.With(guard).Get("/v1/admin/queue-health/owners", h.queueHealthByOwner)
		r.With(guard).Get("/v1/admin/users", h.users)
		r.With(guard).Get("/v1/admin/users/{id}", h.userDetail)
		r.With(guard).Get("/v1/admin/agents", h.agents)
		r.With(guard).Get("/v1/admin/matches", h.matches)
		r.With(guard).Get("/v1/admin/payments", h.payments)
		r.With(guard).Get("/v1/admin/disputes", h.disputes)
		r.With(guard).Get("/v1/admin/overview", h.overview)
		// One developer's wallets, balances and ledger lines — the facts a money
		// support ticket actually turns on. See userwallet.go.
		h.registerWallet(r, guard)
		h.registerAgents(r, guard)
	})
}

func (h *Handler) users(w http.ResponseWriter, r *http.Request) {
	limit, offset := page(r)
	items, err := h.repo.ListUsers(r.Context(), limit, offset)
	writeList(w, "users", items, limit, offset, err)
}

// userDetail serves one developer's operator record.
func (h *Handler) userDetail(w http.ResponseWriter, r *http.Request) {
	d, found, err := h.repo.UserDetail(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if !found {
		httpx.Error(w, httpx.ErrNotFound)
		return
	}
	// Money figures must never be served from a cache — an operator acting on a stale
	// balance is exactly how a double refund happens.
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, d)
}

func (h *Handler) agents(w http.ResponseWriter, r *http.Request) {
	limit, offset := page(r)
	items, err := h.repo.ListAgents(r.Context(), limit, offset)
	writeList(w, "agents", items, limit, offset, err)
}

func (h *Handler) matches(w http.ResponseWriter, r *http.Request) {
	limit, offset := page(r)
	items, err := h.repo.ListMatches(r.Context(), r.URL.Query().Get("status"), limit, offset)
	writeList(w, "matches", items, limit, offset, err)
}

func (h *Handler) payments(w http.ResponseWriter, r *http.Request) {
	limit, offset := page(r)
	items, err := h.repo.ListPayments(r.Context(), limit, offset)
	writeList(w, "payments", items, limit, offset, err)
}

func (h *Handler) disputes(w http.ResponseWriter, r *http.Request) {
	limit, offset := page(r)
	items, err := h.repo.ListDisputes(r.Context(), r.URL.Query().Get("status"), limit, offset)
	writeList(w, "disputes", items, limit, offset, err)
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ov, err := h.repo.Overview(r.Context())
	if err != nil {
		httpx.Error(w, err)
		return
	}
	ov.CoinCents = h.coinCents
	// Left at zero (and ObservedAt absent) when payouts are unconfigured or no
	// reconciliation has landed yet — the consumer renders that as "unknown".
	if h.treasury != nil {
		if bal, lia, at, ok := h.treasury.LastReading(); ok {
			ov.TreasuryUSDCCents, ov.LiabilityUSDCCents, ov.TreasuryObservedAt = bal, lia, at
			// Custody breakdown, when the reader can provide it. Populated only alongside a
			// real observation: describing where the money sits before we have looked once
			// would be inventing a layout.
			if d, okDetail := h.treasury.(TreasuryDetailReader); okDetail {
				fillCustody(&ov, d, lia)
			}
		}
	}
	httpx.JSON(w, http.StatusOK, ov)
}

// fillCustody adds the custody breakdown to an overview that already carries a real
// treasury observation.
//
// liabilityCents is what the platform currently owes on in-flight cash-outs, and it is
// what PayoutsFundedOK is evaluated against: "can the wallet settle everything already
// queued" is the question an operator about to work through the approval list actually
// has. Asking about a zero amount would only ever test the SOL balance.
func fillCustody(ov *Overview, d TreasuryDetailReader, liabilityCents int64) {
	hot, vault, split, ok := d.CustodyBreakdown()
	if !ok {
		return
	}
	ov.HotUSDCCents, ov.VaultUSDCCents, ov.CustodySplit = hot, vault, split

	ov.SweepNeededCents, ov.HotWalletCapCents, ov.ColdWalletAddress = d.SweepNeeded()

	// SOL is reported only when actually measured. Zero-with-a-flag would be
	// indistinguishable from an empty wallet, which is the opposite conclusion.
	if lamports, minLamports, known := d.FeeFuel(); known {
		ov.HotWalletSOL = float64(lamports) / lamportsPerSOL
		ov.HotWalletSOLMin = float64(minLamports) / lamportsPerSOL
		ov.HotWalletSOLLow = minLamports > 0 && lamports < minLamports
	}

	ov.PayoutsFundedOK, ov.PayoutsBlockedReason = d.CanPay(liabilityCents)
}

// writeList emits {"<key>": [...], "limit": N, "offset": M}. A nil slice becomes
// [] so the consumer never has to special-case a missing key.
func writeList[T any](w http.ResponseWriter, key string, items []T, limit, offset int, err error) {
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if items == nil {
		items = []T{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{key: items, "limit": limit, "offset": offset})
}

// page parses ?limit=&offset= with a sane default and hard cap.
func page(r *http.Request) (limit, offset int) {
	limit = defaultPageSize
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v > 0 {
		offset = v
	}
	return limit, offset
}
