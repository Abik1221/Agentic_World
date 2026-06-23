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
// but authenticated by its Stripe signature.
func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		user := auth.RequireScope(auth.ScopeUser)
		r.With(user).Get("/v1/wallet/packs", h.packs)
		r.With(user).Post("/v1/wallet/topup", h.topup)
		r.With(user).Post("/v1/payouts/onboard", h.onboard)
	})
	r.Post("/v1/webhooks/stripe", h.webhook) // public; verified by signature
}

func (h *Handler) packs(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{"packs": h.svc.Packs()})
}

func (h *Handler) topup(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Pack  string `json:"pack"`
		Agent string `json:"agent"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	checkout, err := h.svc.Topup(r.Context(), p.UserPublicID, in.Agent, in.Pack)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"checkout_url": checkout.URL, "session_id": checkout.ID})
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

// webhook verifies and processes an inbound Stripe event. A 4xx (bad signature/
// body) tells Stripe not to retry; a 5xx (processing/DB failure) asks it to.
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
