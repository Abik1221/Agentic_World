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

// Handler exposes the agent-facing matchmaking queue. An agent key or the
// owner's dashboard JWT may sit the account's agent — same rule as rooms.
type Handler struct {
	svc    *Service
	authn  *auth.Authenticator
	stakes stakeResolver
	owners auth.AgentOwner
}

func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

// SetStakeResolver wires the game stake-tier resolver so the queue can accept a
// `tier` and stake at the admin-configured amount. Nil keeps legacy free-form bid.
func (h *Handler) SetStakeResolver(r stakeResolver) { h.stakes = r }

// SetPrimaryAgentLookup lets a dashboard JWT sit the agent /v1/me says they own.
func (h *Handler) SetPrimaryAgentLookup(l auth.AgentOwner) { h.owners = l }

// Register mounts the queue routes:
//
//	POST   /v1/queue   — request an opponent at a bid (enqueue)
//	GET    /v1/queue   — poll status (waiting, or matched + match_id)
//	DELETE /v1/queue   — leave the queue
func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		sit := auth.RequireScopeAny(auth.ScopeAgent, auth.ScopeUser)
		r.With(sit).Post("/v1/queue", h.enqueue)
		r.With(sit).Get("/v1/queue", h.status)
		r.With(sit).Delete("/v1/queue", h.cancel)
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
	// This queue only ever pairs matchmakingGame. It accepted a `game` and then
	// dropped it: Enqueue/Entry carry no game field, so POST {"game":"mafia",
	// "tier":"high"} resolved the stake from MAFIA's tier table and enqueued into the
	// GOOFSPIEL queue at that amount. Silent only because the seeded tiers currently
	// match — the moment an admin raises Mafia's High tier, Goofspiel matches would
	// escrow Mafia money. Reject the mismatch instead of honouring half of it.
	//
	// Ranked Mafia/Monopoly are N-player and live on /v1/group-queue.
	game := in.Game
	if game == "" {
		game = matchmakingGame
	}
	if game != matchmakingGame {
		httpx.Error(w, httpx.NewError(http.StatusBadRequest, "unsupported_game",
			"This queue matchmakes "+matchmakingGame+" only. Use /v1/group-queue for other games."))
		return
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
	agentID, err := auth.SittingAgent(r.Context(), p, h.owners)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	e, err := h.svc.Enqueue(r.Context(), agentID, p.UserPublicID, bid)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, e)
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	agentID, err := auth.SittingAgent(r.Context(), p, h.owners)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	e, err := h.svc.Status(r.Context(), agentID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, e)
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	agentID, err := auth.SittingAgent(r.Context(), p, h.owners)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if err := h.svc.Cancel(r.Context(), agentID); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusNoContent, nil)
}
