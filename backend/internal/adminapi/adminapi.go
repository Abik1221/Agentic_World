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
	Status      string     `json:"status"`
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
}

// Handler serves the admin-read routes.
type Handler struct {
	repo   Repo
	authn  *auth.Authenticator
	admins map[string]bool
}

// NewHandler builds the admin-read handler. adminUserIDs are the user public ids
// (ADMIN_USER_IDS) allowed alongside any valid Platform token.
func NewHandler(repo Repo, authn *auth.Authenticator, adminUserIDs []string) *Handler {
	admins := make(map[string]bool, len(adminUserIDs))
	for _, id := range adminUserIDs {
		admins[id] = true
	}
	return &Handler{repo: repo, authn: authn, admins: admins}
}

// Register mounts the read-only admin routes behind Platform-or-admin auth.
func (h *Handler) Register(r chi.Router) {
	guard := auth.RequirePlatformOrAdmin(h.admins)
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.With(guard).Get("/v1/admin/users", h.users)
		r.With(guard).Get("/v1/admin/agents", h.agents)
		r.With(guard).Get("/v1/admin/matches", h.matches)
		r.With(guard).Get("/v1/admin/payments", h.payments)
		r.With(guard).Get("/v1/admin/disputes", h.disputes)
		r.With(guard).Get("/v1/admin/overview", h.overview)
	})
}

func (h *Handler) users(w http.ResponseWriter, r *http.Request) {
	limit, offset := page(r)
	items, err := h.repo.ListUsers(r.Context(), limit, offset)
	writeList(w, "users", items, limit, offset, err)
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
	httpx.JSON(w, http.StatusOK, ov)
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
