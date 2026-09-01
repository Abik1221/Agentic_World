package match

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/auth"
	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
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
		// Rooms: a private table you share by id, for two developers who want to play
		// each other rather than whoever the queue supplies.
		//
		// Separate routes rather than a flag on /v1/lobby/create, because the lobby
		// routes are live and something else may depend on their exact shape. Joining
		// and cancelling deliberately REUSE the lobby handlers: a room is an ordinary
		// waiting match, and a second join path would be a second place for the escrow
		// and same-owner checks to drift.
		r.With(agent).Post("/v1/room/create", h.createRoom)
		r.With(agent).Get("/v1/match/{id}/state", h.state)
		r.With(agent).Post("/v1/match/{id}/action", h.action)
		// Table talk. Separate from /action on purpose: speaking is not a move, is
		// not turn-gated, and may happen any number of times per round.
		r.With(agent).Post("/v1/match/{id}/say", h.say)
		// Readiness. AGENT-scoped like every other seat action: the thing being asserted is
		// "this agent is present and willing", which only the agent's own credential can say.
		// An owner token must not be able to ready a seat on its agent's behalf — that would
		// let a developer commit a stake for a process that is not actually running, which is
		// the precise situation the ready check exists to prevent.
		r.With(agent).Post("/v1/match/{id}/ready", h.ready)
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

// createRoom opens a private table and returns the code to share.
//
// The response names the field room_id as well as match_id. They are the same value:
// a room IS a match, and inventing a second identifier would mean two ids for one thing
// and a mapping to keep correct. The alias exists because the person reading it is about
// to paste it into a chat window, and "room" is what they will call it.
func (h *Handler) createRoom(w http.ResponseWriter, r *http.Request) {
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
	id, err := h.svc.CreateRoom(r.Context(), p.AgentPublicID, p.UserPublicID, bid)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"room_id": id, "match_id": id, "game": "goofspiel", "bid": bid,
	})
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
		// Rationale is one line of table talk carried BY the move.
		//
		// COST, not decoration. Without it a self-driving agent that wants to speak makes a
		// second model call for the sentence — 26 calls a match instead of 13, which on a free
		// tier of 50 requests/day is the difference between ~1.9 and ~3.8 matches. The
		// platform-driven path has always folded talk into the move (drive.go publishes the
		// same field), and Mafia and Monopoly both do too; this closes the one path that
		// could not.
		//
		// Optional, and never fatal: a rejected or empty line must not cost the agent its
		// card. See below — the move is applied FIRST.
		Rationale string `json:"rationale,omitempty"`
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
	// AFTER the move, deliberately. The card is the thing with stakes on it, and table talk
	// must never be able to fail it: an over-long line, a chat rule, or a race with the round
	// resolving would otherwise turn "I spoke while playing" into "I did not play".
	//
	// Monopoly publishes before the move so spectators watch it argue the deal; here the
	// opponent is simultaneously sealing a card, so speaking first would leak the timing of
	// this seat's decision. Same field, opposite order, for a reason specific to the game.
	if strings.TrimSpace(in.Rationale) != "" {
		if updated, serr := h.svc.Say(r.Context(), p.AgentPublicID, id, in.Rationale, gs.ChatKindRationale); serr == nil {
			view = updated // so the caller sees its own line in the returned transcript
		}
		// A failed line is silent on purpose: the move succeeded, and reporting a chat error
		// as the outcome of a successful play would read as a lost turn.
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

// ready acknowledges that this seat is present and willing to play.
//
// No body: there is nothing to say beyond "I am here", and a payload would invite a future
// where an agent readies with conditions attached.
//
// 204, not 200 with a view. A ready-check match has no state worth returning — it has not
// dealt a turn, and handing back a view would suggest there is something to act on. The agent
// learns the table actually started from match_start, which carries the countdown.
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if err := h.svc.Ready(r.Context(), p.AgentPublicID, chi.URLParam(r, "id")); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusNoContent, nil)
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
	h.writeReplay(w, r, doc)
}

// writeReplay applies the cache policy and writes the document.
//
// Split from the route handler so the policy can be tested directly. It is a decision
// about disclosure as much as cost, and a test that had to stand up a match service to
// reach it would not have been written for every status.
func (h *Handler) writeReplay(w http.ResponseWriter, r *http.Request, doc ReplayDoc) {
	// Caching turns on the match's STATUS, and the split is a correctness one rather
	// than a tuning knob.
	//
	// A FINISHED match's replay is immutable by construction: the event log is closed
	// and ReplayHash is a digest OF that log, published so anyone can verify it. So the
	// hash is exactly the right validator — it changes if and only if the bytes do —
	// and the body can be cached hard and revalidated for free.
	//
	// This is the whole read path for the published clips, and it is the heaviest
	// public document the arena serves: a full event log per view. Uncached, every
	// scrub, replay and shared link re-read and re-encoded it. Cached, a viewer
	// watching one clip repeatedly costs one transfer.
	//
	// An UNFINISHED match must NOT be stored. Its log is REDACTED as it streams —
	// hidden information (Mafia's night, sealed bids) is withheld while it is still
	// secret — so the document is only correct for the moment it was produced. A
	// shared cache holding one would serve a stale view of a live game, and could
	// serve a mid-match snapshot after the information stopped being secret, which is
	// the wrong answer in both directions. no-store, not no-cache: it must not be
	// written down at all.
	if doc.Status == StatusFinished && doc.ReplayHash != "" {
		etag := `"` + doc.ReplayHash + `"`
		w.Header().Set("ETag", etag)
		// immutable so a client with the body does not revalidate at all;
		// stale-while-revalidate so a shared cache never blocks a viewer on an origin
		// round-trip once the body is a day old.
		w.Header().Set("Cache-Control", "public, max-age=3600, stale-while-revalidate=86400, immutable")
		if ifNoneMatch(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
	httpx.JSON(w, http.StatusOK, doc)
}

// ifNoneMatch reports whether the client already holds this entity.
//
// Handles the header's real shape rather than the common case: a comma-separated LIST,
// "*", and the weak "W/" prefix caches are allowed to add. Comparing the raw header to
// the tag would fail to match a legitimate revalidation and re-send the whole event
// log — a cache miss that looks like a cache.
func ifNoneMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(etag, "W/") {
			return true
		}
	}
	return false
}
