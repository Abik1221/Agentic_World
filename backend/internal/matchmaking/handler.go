package matchmaking

import (
	"context"
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// matchmakingGame is the only game the queue currently matchmakes (see the matcher
// + match.Service, which hardcode Goofspiel). The `game` field is accepted so the
// tier lookup is game-scoped and the API is forward-compatible.
const matchmakingGame = "goofspiel"

// stakeResolver maps a chosen game/tier (+ legacy free-form bid) to the coin stake.
// Satisfied by *gamestakes.Service; nil ⇒ legacy free-form bid only.
type stakeResolver interface {
	ResolveStake(ctx context.Context, game, tier string, entryFee int64) (int64, error)
}

// Handler exposes the agent-facing matchmaking queue. All routes require an agent
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
// `tier` and stake at the admin-configured amount. Nil keeps legacy free-form bid.
func (h *Handler) SetStakeResolver(r stakeResolver) { h.stakes = r }

// Register mounts the queue routes:
//
//	POST   /v1/queue   — request an opponent at a bid (enqueue)
//	GET    /v1/queue   — poll status (waiting, or matched + match_id)
//	DELETE /v1/queue   — leave the queue
func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		agent := auth.RequireScope(auth.ScopeAgent)
		r.With(agent).Post("/v1/queue", h.enqueue)
		r.With(agent).Get("/v1/queue", h.status)
		r.With(agent).Delete("/v1/queue", h.cancel)
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
	game := in.Game
	if game == "" {
		game = matchmakingGame
	}
	bid := in.Bid
	if h.stakes != nil {
		// tier chosen → its coins are the bid; free-form bid on a tiered game is
		// rejected (ErrTierRequired); untiered games keep the raw bid.
		b, err := h.stakes.ResolveStake(r.Context(), game, in.Tier, in.Bid)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		bid = b
	}
	e, err := h.svc.Enqueue(r.Context(), p.AgentPublicID, p.UserPublicID, bid)
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
