package monopoly

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/auth"
	mono "github.com/agent-arena/arena/internal/engine/monopoly"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// stakeResolver maps a chosen game/tier (+ legacy free-form fee) to the coin stake.
// Satisfied by *gamestakes.Service; nil ⇒ legacy free-form entry_fee only.
type stakeResolver interface {
	ResolveStake(ctx context.Context, game, tier string, entryFee int64) (int64, error)
}

// Handler exposes the Monopoly spectator and agent APIs. Route shapes match
// Frontend/lib/api.ts.
type Handler struct {
	hub       *Hub
	svc       *Service
	authn     *auth.Authenticator
	stakes    stakeResolver
	heartbeat time.Duration
}

func NewHandler(hub *Hub, svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{hub: hub, svc: svc, authn: authn, heartbeat: 25 * time.Second}
}

// SetStakeResolver wires the game stake-tier resolver so table creation can accept
// a `tier` and stake at the admin-configured amount. Nil keeps legacy free-form.
func (h *Handler) SetStakeResolver(r stakeResolver) { h.stakes = r }

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/monopoly/live", h.live)
	r.Get("/v1/monopoly/{id}/watch", h.watch)
	r.Get("/v1/monopoly/{id}/economy", h.economy)
	r.Get("/v1/monopoly/{id}/replay", h.replay)
	// Public seat → identity for every seat on the board (bots included), so a
	// spectator can label tokens and chat lines.
	r.Get("/v1/monopoly/{id}/roster", h.roster)
	// The board itself, as the ENGINE holds it. Match-independent, hence no {id}.
	r.Get("/v1/monopoly/board", h.board)

	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		agent := auth.RequireScope(auth.ScopeAgent)
		r.With(agent).Get("/v1/monopoly/lobby", h.lobby)
		r.With(agent).Post("/v1/monopoly/lobby/create", h.create)
		r.With(agent).Post("/v1/monopoly/lobby/join", h.join)
		r.With(agent).Post("/v1/monopoly/lobby/cancel", h.cancel)
		r.With(agent).Post("/v1/monopoly/pushplay", h.pushplay)
		r.With(agent).Get("/v1/monopoly/{id}/state", h.state)
		r.With(agent).Post("/v1/monopoly/{id}/action", h.action)
		// Table talk. Separate from /action: speaking is not a move, is not
		// turn-gated, and may happen any number of times per turn.
		r.With(agent).Post("/v1/monopoly/{id}/say", h.say)
	})
}

// pushplay opens a no-stakes table driven by the caller's hosted agent endpoint
// (manifest push model) with engine bots in the other seats; watch it live at
// /v1/monopoly/{id}/watch. Body: {players?}.
func (h *Handler) pushplay(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Players int `json:"players"`
	}
	if r.ContentLength > 0 {
		if err := httpx.DecodeJSON(w, r, &in); err != nil {
			httpx.Error(w, err)
			return
		}
	}
	id, err := h.svc.StartPushPlay(r.Context(), p.AgentPublicID, p.UserPublicID, in.Players)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	h.hub.RegisterMatch(id)
	httpx.JSON(w, http.StatusCreated, map[string]any{"match_id": id, "mode": "sandbox", "driver": "remote"})
}

func (h *Handler) watch(w http.ResponseWriter, r *http.Request) {
	matchID := chi.URLParam(r, "id")
	if h.svc != nil {
		if _, err := h.svc.Replay(r.Context(), matchID); err == nil {
			h.hub.RegisterMatch(matchID)
		}
	}
	s, err := h.hub.Subscribe(matchID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	defer h.hub.Unsubscribe(matchID, s)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)

	lastSeq := parseLastEventID(r)
	for _, fr := range h.hub.backlog(r.Context(), matchID, lastSeq) {
		if fr.seq <= lastSeq {
			continue
		}
		httpx.ArmWriteDeadline(w)
		if _, err := w.Write(fr.data); err != nil {
			return
		}
		lastSeq = fr.seq
	}
	_ = rc.Flush()

	ticker := time.NewTicker(h.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.dead:
			return
		case fr := <-s.ch:
			// Presence frames are unsequenced: write through without dedup and do
			// NOT advance lastSeq, or a resuming client would skip real events.
			if fr.seq == unsequencedSeq {
				httpx.ArmWriteDeadline(w)
				if _, err := w.Write(fr.data); err != nil {
					return
				}
				_ = rc.Flush()
				continue
			}
			if fr.seq <= lastSeq {
				continue
			}
			httpx.ArmWriteDeadline(w)
			if _, err := w.Write(fr.data); err != nil {
				return
			}
			lastSeq = fr.seq
			_ = rc.Flush()
		case <-ticker.C:
			httpx.ArmWriteDeadline(w)
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}

func (h *Handler) live(w http.ResponseWriter, r *http.Request) {
	var rows []LiveMatch
	if h.svc != nil {
		if got, err := h.svc.Live(r.Context()); err == nil {
			rows = got
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=2")
	httpx.JSON(w, http.StatusOK, map[string]any{"matches": h.hub.liveMatches(rows)})
}

func (h *Handler) economy(w http.ResponseWriter, r *http.Request) {
	econ, rewards, err := h.svc.Economy(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"economy": econ, "rewards": rewards})
}

func (h *Handler) replay(w http.ResponseWriter, r *http.Request) {
	timed, roster, err := h.svc.ReplayTimed(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// `at` + `offset_ms` per event so a viewer can reproduce the original pacing.
	var start time.Time
	if len(timed) > 0 {
		start = timed[0].At
	}
	out := make([]map[string]any, 0, len(timed))
	for _, te := range timed {
		out = append(out, map[string]any{
			"seq":       te.Event.Seq,
			"type":      te.Event.Type,
			"payload":   te.Event.Payload,
			"at":        te.At.UTC(),
			"offset_ms": te.At.Sub(start).Milliseconds(),
		})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"events": out, "roster": roster})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Tier     string `json:"tier"`
		EntryFee int64  `json:"entry_fee"`
		Players  int    `json:"players"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	fee := in.EntryFee
	if h.stakes != nil {
		f, err := h.stakes.ResolveStake(r.Context(), GameName, in.Tier, in.EntryFee)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		fee = f
	}
	id, err := h.svc.CreateTable(r.Context(), p.AgentPublicID, p.UserPublicID, fee, in.Players)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	h.hub.RegisterMatch(id)
	httpx.JSON(w, http.StatusCreated, map[string]any{"match_id": id})
}

// lobby lists open waiting (staked, agent-vs-agent) tables the caller can join.
func (h *Handler) lobby(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	entryFee, _ := strconv.ParseInt(r.URL.Query().Get("entry_fee"), 10, 64)
	items, err := h.svc.Lobby(r.Context(), entryFee, p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"matches": items})
}

// join seats the caller's agent at a waiting table; the match starts when it fills.
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
	h.hub.RegisterMatch(in.MatchID)
	httpx.JSON(w, http.StatusOK, view)
}

// cancel aborts a creator's waiting table before it starts.
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
	wait := r.URL.Query().Get("wait") == "true"
	timeout := 15 * time.Second
	if t, err := strconv.Atoi(r.URL.Query().Get("timeout")); err == nil && t > 0 {
		if d := time.Duration(t) * time.Second; d < timeout {
			timeout = d
		}
	}
	view, err := h.svc.State(r.Context(), chi.URLParam(r, "id"), p.AgentPublicID, wait, timeout)
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

// roster returns the public identity of every seat, bots included.
func (h *Handler) roster(w http.ResponseWriter, r *http.Request) {
	seats, err := h.svc.Roster(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"seats": seats, "players": len(seats)})
}

// board serves the canonical 40 squares exactly as the engine holds them —
// prices, the six rent tiers, house cost and mortgage value.
//
// It exists because the in-match console has to show a player what a square will
// actually charge. The client used to have no access to the rent table at all, so
// the only way to fill a "Rent / with 1 house / …" panel was to approximate it
// (the spectator view's rentOf() does exactly that, and its own comment calls it
// out: an approximation must never be shown on a live table, because a player
// staking real coins would be reading a number the engine will never charge).
//
// Serving the engine's own table removes the choice. There is no second copy to
// drift: this is engine.Board(), marshalled through the struct tags the engine
// already carries. Mortgage value is computed by the engine's own rule rather
// than divided by two here, for the same reason.
//
// Railroad and utility rent are NOT in Space.Rent — they depend on how many of
// the set the owner holds (rails) or on the dice (utilities), so the engine
// computes them. Both models are published here so the console can state the
// real rule instead of leaving those eight squares blank.
func (h *Handler) board(w http.ResponseWriter, r *http.Request) {
	spaces := mono.Board()
	out := make([]map[string]any, 0, len(spaces))
	for _, sp := range spaces {
		row := map[string]any{"index": sp.Index, "name": sp.Name, "kind": string(sp.Kind)}
		if sp.Group != "" {
			row["group"] = sp.Group
		}
		if sp.Price > 0 {
			row["price"] = sp.Price
			row["mortgage"] = sp.MortgageValue()
		}
		if sp.Kind == mono.KindStreet {
			row["rent"] = sp.Rent
			row["house_cost"] = sp.HouseCost
		}
		if sp.Tax > 0 {
			row["tax"] = sp.Tax
		}
		out = append(out, row)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"spaces": out,
		// The two rent models the table cannot express, named so a client shows the
		// rule rather than an invented figure.
		"railroad_rent":     mono.RailroadRentTable(),
		"utility_multiples": mono.UtilityMultiples(),
		"hotel_houses":      5,
	})
}

// say posts one line of public table talk. No turn is required: an agent may
// speak at any point in a live match, and a line never counts as a move.
func (h *Handler) say(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Text string `json:"text"`
		Kind string `json:"kind"` // "say" (default) | "rationale"
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	view, err := h.svc.Say(r.Context(), p.AgentPublicID, chi.URLParam(r, "id"), in.Text, in.Kind)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

func (h *Handler) action(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Action   string      `json:"action"`
		Property int         `json:"property"`
		Amount   int         `json:"amount"`
		Trade    *mono.Trade `json:"trade"`
		// Optional Ed25519 signature over the canonical (match, next_seq, seat,
		// action) message. Required when the agent registered a signing key.
		Signature string `json:"signature"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	act := mono.Action{Kind: in.Action, Property: in.Property, Amount: in.Amount, Trade: in.Trade}
	view, err := h.svc.Act(r.Context(), p.AgentPublicID, chi.URLParam(r, "id"), act, in.Signature, false)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

func parseLastEventID(r *http.Request) int {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("last_event_id")
	}
	if raw == "" {
		return -1
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return n
}
