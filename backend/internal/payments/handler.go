package payments

import (
	"io"
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

const maxWebhookBody = 1 << 20 // 1 MiB

// Handler exposes the buyer-facing top-up/onboarding endpoints (user scope) and
// the inbound Stripe webhook (public, signature-verified).
type Handler struct {
	svc   *Service
	authn *auth.Authenticator
}

func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

// Register mounts the routes. Top-up and onboarding require a USER (dashboard)
// token — an agent credential can never move real money. The webhook is public
// but authenticated by its Stripe signature. Dev routes are for testing only.
func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		user := auth.RequireScope(auth.ScopeUser)
		r.With(user).Get("/v1/wallet/packs", h.packs)
		r.With(user).Get("/v1/wallet/packs/{key}/quote", h.quote)
		r.With(user).Post("/v1/wallet/topup", h.topup)
		r.With(user).Post("/v1/wallet/topup/confirm", h.confirm)
		r.With(user).Post("/v1/payouts/onboard", h.onboard)
		// Dev-only: manually confirm a dev checkout (useful for testing error
		// handling when webhooks are delayed/lost). NEVER mounted in prod — there
		// it would let any logged-in user mint coins with no Stripe charge. The
		// handler also re-checks DevMode as defense-in-depth.
		if h.svc.cfg.DevMode {
			r.With(user).Post("/v1/admin/dev/confirm-checkout", h.devConfirmCheckout)
		}
	})
	r.Post("/v1/webhooks/stripe", h.webhook)
}

func (h *Handler) packs(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{"packs": h.svc.Packs()})
}

func (h *Handler) quote(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Query().Get("payment_method")
	q, err := h.svc.QuotePack(chi.URLParam(r, "key"), method)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, q)
}

func (h *Handler) topup(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Pack          string `json:"pack"`
		Agent         string `json:"agent"`
		PaymentMethod string `json:"payment_method"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	checkout, err := h.svc.Topup(r.Context(), p.UserPublicID, in.Agent, in.Pack, in.PaymentMethod)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"checkout_url": checkout.URL, "session_id": checkout.ID})
}

func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		SessionID string `json:"session_id"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if err := h.svc.ConfirmDevCheckout(r.Context(), p.UserPublicID, in.SessionID); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"status": "credited"})
}

func (h *Handler) onboard(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	link, err := h.svc.Onboard(r.Context(), p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"onboarding_url": link})
}

func (h *Handler) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		httpx.Error(w, httpx.ErrBadRequest)
		return
	}
	if err := h.svc.HandleWebhook(r.Context(), body, r.Header.Get("Stripe-Signature")); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"received": true})
}

// devConfirmCheckout is a testing-only endpoint to manually confirm a checkout.
// In production this is not available; real purchases are confirmed via Stripe webhooks.
// This endpoint is useful for testing error handling when webhooks are delayed/lost.
func (h *Handler) devConfirmCheckout(w http.ResponseWriter, r *http.Request) {
	// Defense-in-depth: this route is only mounted in DevMode, but never mint
	// coins outside dev even if it were somehow reachable.
	if !h.svc.cfg.DevMode {
		httpx.Error(w, httpx.ErrNotFound)
		return
	}
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		SessionID string `json:"session_id"`
		Agent     string `json:"agent"`
		Coins     int64  `json:"coins"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.SessionID == "" || in.Agent == "" || in.Coins <= 0 {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "invalid_input", "session_id, agent, and coins are required"))
		return
	}
	// Verify user owns the agent
	owner, err := h.svc.Repo.OwnerOfAgent(r.Context(), in.Agent)
	if err != nil {
		httpx.Error(w, httpx.ErrNotFound)
		return
	}
	if owner != p.UserPublicID {
		httpx.Error(w, httpx.NewError(http.StatusForbidden, "forbidden", "You do not own this agent"))
		return
	}
	// Simulate the webhook: credit the OWNER's treasury wallet (Topup keys on the
	// USER public id, exactly like the real checkout.session.completed path — the
	// agent id is only used above to authorize this dev call), idempotent by session.
	if err := h.svc.Coiner.Topup(r.Context(), owner, in.Coins, "topup:"+in.SessionID); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"confirmed": true, "session_id": in.SessionID, "coins": in.Coins})
}
