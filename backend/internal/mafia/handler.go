package mafia

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/auth"
	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes Mafia spectator and agent APIs.
// stakeResolver maps a chosen game/tier (+ legacy free-form fee) to the coin stake
// to use. Satisfied by *gamestakes.Service; nil ⇒ legacy free-form entry_fee only.
type stakeResolver interface {
	ResolveStake(ctx context.Context, game, tier string, entryFee int64) (int64, error)
}

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
	r.Get("/v1/mafia/live", h.live)
	r.Get("/v1/mafia/{id}/watch", h.watch)
	r.Get("/v1/mafia/{id}/economy", h.economy)
	// Public seat → agent identity. Spectators need this to label the table: the
	// event stream carries seat numbers only, so without it a viewer cannot show
	// who is speaking, who is being voted for, or whose avatar to light up.
	r.Get("/v1/mafia/{id}/roster", h.roster)
	r.Get("/v1/mafia/{id}/replay", h.replay)

	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		agent := auth.RequireScope(auth.ScopeAgent)
		r.With(agent).Get("/v1/mafia/lobby", h.lobby)
		r.With(agent).Post("/v1/mafia/lobby/create", h.create)
		r.With(agent).Post("/v1/mafia/lobby/join", h.join)
		r.With(agent).Post("/v1/mafia/lobby/cancel", h.cancel)
		r.With(agent).Post("/v1/mafia/pushplay", h.pushplay)
		r.With(agent).Get("/v1/mafia/{id}/state", h.state)
		r.With(agent).Post("/v1/mafia/{id}/action", h.action)
		// Free-form table talk during discussion. Separate from /action: it does
		// not consume the seat's formal statement and may be called repeatedly.
		r.With(agent).Post("/v1/mafia/{id}/say", h.say)
	})
}

// pushplay opens a no-stakes 12-seat table driven by the caller's hosted endpoint
// (seat 1) with rule-based bots in the other seats; watch it live at
// /v1/mafia/{id}/watch.
func (h *Handler) pushplay(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	id, err := h.svc.StartPushPlay(r.Context(), p.AgentPublicID, p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
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
			// Presence frames (thinking indicators) are unsequenced: they must be
			// written through without dedup and must NOT advance lastSeq, or a
			// resuming client would skip real game events after them.
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
	var extra []LiveMatch
	if h.svc != nil {
		if rows, err := h.svc.Live(r.Context()); err == nil {
			extra = rows
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=2")
	httpx.JSON(w, http.StatusOK, map[string]any{"matches": h.hub.liveMatches(extra)})
}

// roster returns the public identity of every seat. Safe for anyone to read: it
// carries names and avatars, never roles or any other hidden state.
func (h *Handler) roster(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		httpx.Error(w, httpx.ErrNotFound)
		return
	}
	seats, err := h.svc.Roster(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"seats": seats, "players": len(seats)})
}

func (h *Handler) economy(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		httpx.Error(w, httpx.ErrNotFound)
		return
	}
	econ, rewards, err := h.svc.Economy(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"economy": econ, "rewards": rewards})
}

func (h *Handler) replay(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		httpx.Error(w, httpx.ErrNotFound)
		return
	}
	timed, roster, err := h.svc.ReplayTimed(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// Each event carries `at` (absolute) and `offset_ms` (from the first event), so a
	// player can reproduce the original pacing without doing clock arithmetic — the
	// pauses between lines are part of the record, not decoration.
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

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Tier     string `json:"tier"`
		EntryFee int64  `json:"entry_fee"`
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
	id, err := h.svc.CreateTable(r.Context(), p.AgentPublicID, p.UserPublicID, fee)
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
	h.hub.RegisterMatch(in.MatchID)
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

// say posts one line of free-form table talk. Legal only while the floor is open
// (discussion): the town is asleep at night and the ballot is closed during voting.
// It never consumes the seat's formal statement and never advances the phase, so an
// agent can argue back the moment it is accused.
func (h *Handler) say(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Text   string `json:"text"`
		Tone   string `json:"tone"`   // accuse | defend | claim | info | alliance
		Target int    `json:"target"` // optional seat this line is aimed at
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	view, err := h.svc.Say(r.Context(), p.AgentPublicID, chi.URLParam(r, "id"), in.Text, in.Tone, in.Target)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

func (h *Handler) action(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Action string `json:"action"`
		Target int    `json:"target"`
		Tone   string `json:"tone"`
		Text   string `json:"text"`
		// Optional stale-phase guard: the (day, phase) the client saw when it chose
		// this action. If the match has since advanced, the action is rejected. (G1)
		ExpectedDay   int    `json:"expected_day"`
		ExpectedPhase string `json:"expected_phase"`
		// Optional Ed25519 signature over the canonical (match, day, seat, action)
		// message. Required when the agent registered a signing key.
		Signature string `json:"signature"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	act := mf.Action{Kind: in.Action, Target: in.Target, Tone: in.Tone, Text: in.Text}
	view, err := h.svc.Act(r.Context(), p.AgentPublicID, chi.URLParam(r, "id"), act, in.ExpectedDay, in.ExpectedPhase, in.Signature, false)
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
