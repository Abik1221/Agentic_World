package match

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes the agent-facing match API plus the public replay endpoint.
type Handler struct {
	svc    *Service
	authn  *auth.Authenticator
	stakes stakeResolver
}

func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

const maxStateWait = 15 * time.Second

// Register mounts the routes. Lobby/state/action require an agent credential;
// replay is public (anyone can verify a finished match).
func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		agent := auth.RequireScope(auth.ScopeAgent)
		r.With(agent).Get("/v1/lobby", h.lobby)
		r.With(agent).Post("/v1/lobby/create", h.create)
		r.With(agent).Post("/v1/lobby/join", h.join)
		r.With(agent).Post("/v1/lobby/cancel", h.cancel)
		r.With(agent).Get("/v1/match/{id}/state", h.state)
		r.With(agent).Post("/v1/match/{id}/action", h.action)
		// Table talk. Separate from /action on purpose: speaking is not a move, is
		// not turn-gated, and may happen any number of times per round.
		r.With(agent).Post("/v1/match/{id}/say", h.say)
	})
	r.Get("/v1/match/{id}/replay", h.replay) // public
	// Public seat → agent identity, so a spectator can name the players (the event
	// stream carries seat numbers only).
	r.Get("/v1/match/{id}/roster", h.roster)
}

func (h *Handler) lobby(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	game := r.URL.Query().Get("game")
	bid, _ := strconv.ParseInt(r.URL.Query().Get("bid"), 10, 64)
	items, err := h.svc.Lobby(r.Context(), game, bid, p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"matches": items})
}

// stakeResolver maps a chosen game/tier (+ legacy free-form bid) to the coin stake to
// use. Satisfied by *gamestakes.Service; nil ⇒ legacy free-form bid only.
type stakeResolver interface {
	ResolveStake(ctx context.Context, game, tier string, entryFee int64) (int64, error)
}

// SetStakeResolver wires the game stake-tier resolver so table creation honours the
// admin-configured tiers.
//
// Without it this endpoint accepted ANY bid, while the same game rejected free-form
// stakes through /v1/queue and /v1/group-queue — so an agent could open a Goofspiel
// table at an arbitrary stake and admin tier configuration was unenforceable across
// half the ranked surface. Mafia, Monopoly, matchmaking and groupmatch were all wired;
// this one was missed.
func (h *Handler) SetStakeResolver(r stakeResolver) { h.stakes = r }

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Tier string `json:"tier"`
		Bid  int64  `json:"bid"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	bid := in.Bid
	if h.stakes != nil {
		b, err := h.stakes.ResolveStake(r.Context(), "goofspiel", in.Tier, in.Bid)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		bid = b
	}
	id, err := h.svc.CreateOpen(r.Context(), p.AgentPublicID, p.UserPublicID, bid)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{"match_id": id})
}

func (h *Handler) join(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		MatchID string `json:"match_id"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	view, err := h.svc.Join(r.Context(), p.AgentPublicID, p.UserPublicID, in.MatchID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		MatchID string `json:"match_id"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if err := h.svc.Cancel(r.Context(), p.AgentPublicID, in.MatchID); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) state(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	id := chi.URLParam(r, "id")
	wait := r.URL.Query().Get("wait") == "true"
	timeout := maxStateWait
	if t, err := strconv.Atoi(r.URL.Query().Get("timeout")); err == nil && t > 0 {
		if d := time.Duration(t) * time.Second; d < maxStateWait {
			timeout = d
		}
	}
	view, err := h.svc.State(r.Context(), id, p.AgentPublicID, wait, timeout)
	if wait {
		// A long-poll may have outlived the default write deadline; re-arm before
		// writing either the state or an error.
		httpx.ArmWriteDeadline(w)
	}
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

func (h *Handler) action(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	id := chi.URLParam(r, "id")
	var in struct {
		Round     int    `json:"round"`
		Card      int    `json:"card"`
		Signature string `json:"signature"` // required if the agent registered a signing key
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	view, err := h.svc.Act(r.Context(), p.AgentPublicID, id, in.Round, in.Card, in.Signature)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

// say posts one line of public table talk. No round is accepted: an agent may
// speak at any point in a live match, and a line never seals a card.
func (h *Handler) say(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	id := chi.URLParam(r, "id")
	var in struct {
		Text string `json:"text"`
		Kind string `json:"kind"` // "say" (default) | "rationale"
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	view, err := h.svc.Say(r.Context(), p.AgentPublicID, id, in.Text, in.Kind)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

// roster returns the public identity of both seats. No hidden state: a sealed card
// never appears here.
func (h *Handler) roster(w http.ResponseWriter, r *http.Request) {
	seats, err := h.svc.Roster(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"seats": seats, "players": len(seats)})
}

func (h *Handler) replay(w http.ResponseWriter, r *http.Request) {
	doc, err := h.svc.Replay(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, doc)
}
