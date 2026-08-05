package groupmatch

import (
	"context"
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// stakeResolver maps a chosen game/tier (+ legacy free-form bid) to the coin stake,
// so the group queue stakes at the admin-configured tier amount just like the 2-player
// queue. Satisfied by *gamestakes.Service; nil ⇒ legacy free-form bid only.
type stakeResolver interface {
	ResolveStake(ctx context.Context, game, tier string, entryFee int64) (int64, error)
}

// Handler exposes the agent-facing N-player group queue. All routes require an agent
// credential (only an agent queues itself; an owner token cannot).
type Handler struct {
	svc    *Service
	authn  *auth.Authenticator
	stakes stakeResolver
}

func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

// SetStakeResolver wires the game stake-tier resolver so the queue can accept a
// `tier` and stake at the admin-configured amount. Nil keeps the legacy free-form bid.
func (h *Handler) SetStakeResolver(r stakeResolver) { h.stakes = r }

// Register mounts the group-queue routes (sibling of /v1/queue, for N-player games):
//
//	POST   /v1/group-queue   — request a seat in game at a bid/tier (enqueue)
//	GET    /v1/group-queue   — poll status (waiting, or matched + match_id), with the
//	                           caller's position in its pool, how many distinct owners
//	                           are waiting, the seat target, and how long until a
//	                           short-handed table may start
//	DELETE /v1/group-queue   — leave the queue
func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		agent := auth.RequireScope(auth.ScopeAgent)
		r.With(agent).Post("/v1/group-queue", h.enqueue)
		r.With(agent).Get("/v1/group-queue", h.status)
		r.With(agent).Delete("/v1/group-queue", h.cancel)
	})
}

func (h *Handler) enqueue(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Game string `json:"game"`
		Tier string `json:"tier"`
		Bid  int64  `json:"bid"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.Game == "" {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "invalid_request", "game is required"))
		return
	}
	bid := in.Bid
	if h.stakes != nil {
		b, err := h.stakes.ResolveStake(r.Context(), in.Game, in.Tier, in.Bid)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		bid = b
	}
	e, err := h.svc.Enqueue(r.Context(), p.AgentPublicID, p.UserPublicID, in.Game, bid)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, e)
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	e, err := h.svc.Status(r.Context(), p.AgentPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, e)
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if err := h.svc.Cancel(r.Context(), p.AgentPublicID); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusNoContent, nil)
}
