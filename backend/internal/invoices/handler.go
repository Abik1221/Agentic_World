package invoices

import (
	"net/http"
	"strconv"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler serves the user's own receipts. User scope only; the owner comes from
// the token and the id is re-checked against it in the query, so someone else's
// invoice id is a 404 rather than a leak.
type Handler struct {
	svc   *Service
	authn *auth.Authenticator
}

func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		user := auth.RequireScope(auth.ScopeUser)
		r.With(user).Get("/v1/user/invoices", h.list)
		r.With(user).Get("/v1/user/invoices/{id}", h.get)
	})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	items, err := h.svc.List(r.Context(), p.UserPublicID, limit, offset)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if items == nil {
		items = []Invoice{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"invoices": items, "issuer": h.svc.Issuer()})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	inv, found, err := h.svc.Get(r.Context(), p.UserPublicID, chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if !found {
		httpx.Error(w, httpx.ErrNotFound)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"invoice": inv, "issuer": h.svc.Issuer()})
}
