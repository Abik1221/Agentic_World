package autoplay

import (
	"context"
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// RankedGate validates, at enable time, that an agent may turn on ranked auto-play
// for the game it would play — that its manifest declares that game. Optional; nil ⇒
// skip (the queue's own enqueue gate still enforces this per match, so money is safe
// either way — this just gives the owner immediate feedback instead of silent
// never-playing). Satisfied by an adapter over manifest.Service.
type RankedGate interface {
	CheckRankedGame(ctx context.Context, agentPublicID, game string) error
}

// Handler exposes an agent's auto-play settings: GET to read, PUT to set. The
// agent (and its owner) are read from the token, never the body — a dev can only
// configure their own agent's availability.
type Handler struct {
	repo   Repo
	authn  *auth.Authenticator
	ranked RankedGate // optional enable-time ranked-eligibility check
}

func NewHandler(repo Repo, authn *auth.Authenticator) *Handler {
	return &Handler{repo: repo, authn: authn}
}

// SetRankedGate installs the enable-time ranked-eligibility check (call once at wiring).
func (h *Handler) SetRankedGate(g RankedGate) { h.ranked = g }

func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		agent := auth.RequireScope(auth.ScopeAgent)
		r.With(agent).Get("/v1/agent/autoplay", h.get)
		r.With(agent).Put("/v1/agent/autoplay", h.set)
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	s, ok, err := h.repo.Get(r.Context(), p.AgentPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if !ok {
		httpx.JSON(w, http.StatusOK, Setting{AgentPublicID: p.AgentPublicID, Enabled: false, Mode: ModeSandbox})
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}

func (h *Handler) set(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Enabled          bool     `json:"enabled"`
		Mode             string   `json:"mode"`
		Bid              int64    `json:"bid"`
		Games            []string `json:"games"`
		ActiveFromUTC    int      `json:"active_from_utc"`
		ActiveUntilUTC   int      `json:"active_until_utc"`
		DailyMatchCap    int      `json:"daily_match_cap"`
		DailyTokenBudget int64    `json:"daily_token_budget"`
		TakeProfitCoins  int64    `json:"take_profit_coins"`
		DailyLossStop    int64    `json:"daily_loss_stop"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	mode := Mode(in.Mode)
	if mode != ModeRanked && mode != ModeSandbox {
		mode = ModeSandbox // default to the free, no-stakes arena
	}
	// Reject enabling ranked auto-play for an agent that can't play the ranked game
	// (Goofspiel-only today) — fail fast with a clear message rather than accepting
	// the setting and then never staking a match.
	if in.Enabled && mode == ModeRanked && h.ranked != nil {
		if err := h.ranked.CheckRankedGame(r.Context(), p.AgentPublicID, rankedGameOf(in.Games)); err != nil {
			httpx.Error(w, err)
			return
		}
	}
	clampHour := func(h int) int {
		if h < 0 {
			return 0
		}
		if h > 23 {
			return 23
		}
		return h
	}
	nonNeg := func(v int64) int64 {
		if v < 0 {
			return 0
		}
		return v
	}
	// games is a NOT NULL text[] (empty ⇒ rotate through the defaults); a client
	// that sends no games must persist as an empty array, not SQL NULL.
	games := in.Games
	if games == nil {
		games = []string{}
	}
	s := Setting{
		AgentPublicID:    p.AgentPublicID,
		OwnerPublicID:    p.UserPublicID,
		Enabled:          in.Enabled,
		Mode:             mode,
		Bid:              in.Bid,
		Games:            games,
		ActiveFromUTC:    clampHour(in.ActiveFromUTC),
		ActiveUntilUTC:   clampHour(in.ActiveUntilUTC),
		DailyMatchCap:    max(0, in.DailyMatchCap),
		DailyTokenBudget: nonNeg(in.DailyTokenBudget),
		TakeProfitCoins:  nonNeg(in.TakeProfitCoins),
		DailyLossStop:    nonNeg(in.DailyLossStop),
	}
	if err := h.repo.Set(r.Context(), s); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, s)
}
