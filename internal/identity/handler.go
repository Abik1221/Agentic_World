package identity

import (
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/middleware"
	"github.com/go-chi/chi/v5"
)

// Handler exposes the identity HTTP API and mounts it with the correct auth
// scopes. registerRL is the rate-limit middleware applied to registration
// (5/hour/IP), injected so the handler stays decoupled from the limiter backend.
type Handler struct {
	svc        *Service
	authn      *auth.Authenticator
	registerRL func(http.Handler) http.Handler
}

func NewHandler(svc *Service, authn *auth.Authenticator, registerRL func(http.Handler) http.Handler) *Handler {
	if registerRL == nil {
		registerRL = func(n http.Handler) http.Handler { return n } // no-op fallback
	}
	return &Handler{svc: svc, authn: authn, registerRL: registerRL}
}

// Register is an httpx.Mount: it attaches all identity routes with their guards.
func (h *Handler) Register(r chi.Router) {
	// Public onboarding (rate-limited).
	r.Group(func(r chi.Router) {
		r.Use(h.registerRL)
		r.Post("/v1/register", h.register)
		r.Get("/v1/register/verify", h.verify)
	})

	// Authenticated routes: attach the principal, then guard by scope.
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/agent/config", h.updateConfig)
		r.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/agent/keys", h.createKey)
		r.With(auth.RequireScope(auth.ScopeUser)).Delete("/v1/agent/keys/{prefix}", h.revokeKey)
		r.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/agent/signing-key", h.setSigningKey)
		// /v1/agent/stats is served by the profiles module (Stage 7, real data).
	})
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AgentName   string `json:"agent_name"`
		Description string `json:"description"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	claim, err := h.svc.Register(r.Context(), in.AgentName, in.Description)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"claim_token": claim.Token,
		"expires_at":  claim.ExpiresAt,
		"instructions": "Post a public tweet containing this claim token, then poll " +
			"GET /v1/register/verify?claim_token=" + claim.Token,
	})
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("claim_token")
	if token == "" {
		httpx.Error(w, errInvalid("claim_token query parameter is required"))
		return
	}
	captchaToken := r.URL.Query().Get("captcha")
	if captchaToken == "" {
		captchaToken = r.Header.Get("X-Captcha-Token")
	}

	res, err := h.svc.VerifyClaim(r.Context(), token, captchaToken, clientIP(r))
	if err != nil {
		httpx.Error(w, err) // includes the 202 "claim_pending" polling state
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"api_key":         res.APIKey,
		"agent_id":        res.AgentID,
		"dashboard_token": res.DashboardToken,
	})
}

func (h *Handler) updateConfig(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		AgentID              string `json:"agent_id"`
		CoinLimitPerMatch    int64  `json:"coin_limit_per_match"`
		DailyLossLimit       int64  `json:"daily_loss_limit"`
		SessionLossLimit     int64  `json:"session_loss_limit"`
		MinWalletBalance     int64  `json:"min_wallet_balance"`
		MaxBid               int64  `json:"max_bid"`
		MaxConcurrentMatches int    `json:"max_concurrent_matches"`
		CooldownLosses       int    `json:"cooldown_losses"`
		CooldownSeconds      int    `json:"cooldown_seconds"`
		AutoJoin             bool   `json:"auto_join"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.AgentID == "" {
		httpx.Error(w, errInvalid("agent_id is required"))
		return
	}
	limits := Limits{
		CoinLimitPerMatch: in.CoinLimitPerMatch, DailyLossLimit: in.DailyLossLimit,
		SessionLossLimit: in.SessionLossLimit, MinWalletBalance: in.MinWalletBalance,
		MaxBid: in.MaxBid, MaxConcurrentMatches: in.MaxConcurrentMatches,
		CooldownLosses: in.CooldownLosses, CooldownSeconds: in.CooldownSeconds, AutoJoin: in.AutoJoin,
	}
	if err := h.svc.UpdateConfig(r.Context(), p.UserPublicID, in.AgentID, limits); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		AgentID string `json:"agent_id"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.AgentID == "" {
		httpx.Error(w, errInvalid("agent_id is required"))
		return
	}
	raw, err := h.svc.RotateKey(r.Context(), p.UserPublicID, in.AgentID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]string{"api_key": raw})
}

func (h *Handler) setSigningKey(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		AgentID string `json:"agent_id"`
		Pubkey  string `json:"pubkey"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.AgentID == "" {
		httpx.Error(w, errInvalid("agent_id is required"))
		return
	}
	if err := h.svc.SetSigningKey(r.Context(), p.UserPublicID, in.AgentID, in.Pubkey); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "registered"})
}

func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	prefix := chi.URLParam(r, "prefix")
	if err := h.svc.RevokeKey(r.Context(), p.UserPublicID, prefix); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusNoContent, nil)
}

// stats is a Stage 1 stub; real numbers arrive in Stage 7 (ratings/profiles).

// clientIP extracts a best-effort client IP for captcha/rate-limit purposes.
func clientIP(r *http.Request) string {
	if id := middleware.ClientIP(r); id != "" {
		return id
	}
	return r.RemoteAddr
}
