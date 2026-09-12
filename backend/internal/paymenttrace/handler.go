package paymenttrace

import (
	"net/http"
	"strconv"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler serves the payment telemetry surface.
//
// User scope, identity from the token. There is no id parameter on the user
// routes for the same reason the event stream has none: a payment timeline names
// amounts, wallet addresses and on-chain signatures, so a route that accepted
// "whose" would be an IDOR waiting to happen.
type Handler struct {
	svc   *Service
	authn *auth.Authenticator
	// admins is the allowlist for the operator-wide failure feed.
	admins map[string]bool
}

func NewHandler(svc *Service, authn *auth.Authenticator, admins map[string]bool) *Handler {
	return &Handler{svc: svc, authn: authn, admins: admins}
}

func (h *Handler) Register(r chi.Router) {
	// The flow models are documentation, not data: they describe how a deposit
	// works and are the same for everyone. Public so the UI can draw the diagram
	// before a user has ever paid — the explainer and the diagnostic are one
	// component, and it should not need a session to render.
	r.Get("/v1/payments/flows", h.flows)

	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		user := auth.RequireScope(auth.ScopeUser)
		r.With(user).Get("/v1/payments/timeline", h.timeline)
		r.With(user).Get("/v1/payments/timeline/{flow}/{ref}", h.one)
		// Operator triage: every broken payment, everyone. No user-scope guard —
		// RequirePlatformOrAdmin admits the Super Admin's Platform token, which
		// carries no user scope and would be rejected by one.
		r.With(auth.RequirePlatformOrAdmin(h.admins)).Get("/v1/admin/payments/failures", h.failures)
	})
}

func (h *Handler) flows(w http.ResponseWriter, r *http.Request) {
	// Cacheable: this is a static description of the product, identical for every
	// caller and carrying nothing about anyone.
	w.Header().Set("Cache-Control", "public, max-age=300")
	httpx.JSON(w, http.StatusOK, map[string]any{"flows": Specs()})
}

func (h *Handler) timeline(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := h.svc.Timelines(r.Context(), p.UserPublicID, limit)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if items == nil {
		items = []Timeline{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"payments": items})
}

// one returns a single flow's timeline. Scoped to the caller in the query itself,
// so an id belonging to someone else returns 404 rather than their data.
func (h *Handler) one(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	flow, ref := chi.URLParam(r, "flow"), chi.URLParam(r, "ref")
	events, err := h.svc.repo.ByRef(r.Context(), p.UserPublicID, flow, ref)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if len(events) == 0 {
		httpx.Error(w, httpx.ErrNotFound)
		return
	}
	tls := Assemble(events, nowUTC())
	httpx.JSON(w, http.StatusOK, tls[0])
}

func (h *Handler) failures(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := h.svc.repo.RecentFailures(r.Context(), limit)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if items == nil {
		items = []FailureRow{}
	}
	sum, err := h.svc.repo.MoneyTrace(r.Context())
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// Never cached: an operator acting on a stale failure list re-investigates
	// something already fixed, or misses one that just broke.
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{"failures": items, "summary": sum})
}
