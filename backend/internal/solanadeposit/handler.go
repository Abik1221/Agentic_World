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
}

// NewHandler wires the deposit HTTP surface.
func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

// Register mounts the deposit routes under user-scope auth.
func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/deposits", h.create)
		r.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/deposits", h.list)
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
