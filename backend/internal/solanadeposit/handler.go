package solanadeposit

import (
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes the deposit API (user scope only — deposits credit the caller's
// own treasury, identity taken from the token, never the body).
type Handler struct {
	svc   *Service
	authn *auth.Authenticator
	rl    func(http.Handler) http.Handler
}

// NewHandler wires the deposit HTTP surface.
func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn, rl: passthrough}
}

// SetRateLimit installs a per-user limiter on deposit-session creation (each call
// mints a fresh Solana Pay reference + DB row and enlarges the listener scan, so
// it must be bounded). Nil keeps the no-op passthrough.
func (h *Handler) SetRateLimit(mw func(http.Handler) http.Handler) {
	if mw != nil {
		h.rl = mw
	}
}

func passthrough(next http.Handler) http.Handler { return next }

// Register mounts the deposit routes under user-scope auth.
func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.With(auth.RequireScope(auth.ScopeUser), h.rl).Post("/v1/deposits", h.create)
		r.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/deposits", h.list)
		// Static route registered before the {id} wildcard (chi prefers static anyway).
		r.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/deposits/config", h.config)
		r.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/deposits/{id}", h.get)
	})
}

// sessionJSON renders a session plus the Solana Pay fields the client needs to
// build the transfer + QR code.
func (h *Handler) sessionJSON(s Session) map[string]any {
	return map[string]any{
		"deposit_id":      s.PublicID,
		"reference":       s.Reference,
		"recipient":       h.svc.Recipient(),
		"spl_token":       h.svc.USDCMint(),
		"asset":           s.Asset,
		"amount_base":     s.AmountExpected,
		"coins_expected":  s.CoinsExpected,
		"status":          s.Status,
		"tx_signature":    s.TxSignature,
		"amount_received": s.AmountReceived,
		"coins_credited":  s.CoinsCredited,
		"pay_url":         h.svc.PayURL(s),
		"created_at":      s.CreatedAt,
		"expires_at":      s.ExpiresAt,
	}
}

// config serves GET /v1/deposits/config — the mint / recipient / peg the client needs
// to check the payer's on-chain balance BEFORE creating a deposit session. Without it
// the UI can only discover "you have no USDC" after the wallet popup fails.
func (h *Handler) config(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"spl_token":      h.svc.USDCMint(),
		"recipient":      h.svc.Recipient(),
		"asset":          "USDC",
		"decimals":       h.svc.Decimals(),
		"coins_per_usdc": h.svc.CoinsPerUSDC(),
	})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		AmountUSDC float64 `json:"amount_usdc"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	base := h.svc.USDCToBase(in.AmountUSDC)
	sess, err := h.svc.Create(r.Context(), p.UserPublicID, base)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, h.sessionJSON(sess))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	sess, err := h.svc.Get(r.Context(), chi.URLParam(r, "id"), p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, h.sessionJSON(sess))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	sessions, err := h.svc.List(r.Context(), p.UserPublicID, 20)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, h.sessionJSON(s))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"deposits": out})
}
