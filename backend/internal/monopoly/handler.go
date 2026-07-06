package monopoly

import (
	"net/http"
	"strconv"
	"time"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes the Monopoly spectator and agent APIs. Route shapes match
// Frontend/lib/api.ts.
type Handler struct {
	hub       *Hub
	svc       *Service
	authn     *auth.Authenticator
	heartbeat time.Duration
}

func NewHandler(hub *Hub, svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{hub: hub, svc: svc, authn: authn, heartbeat: 25 * time.Second}
}

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/monopoly/live", h.live)
	r.Get("/v1/monopoly/{id}/watch", h.watch)
	r.Get("/v1/monopoly/{id}/economy", h.economy)
	r.Get("/v1/monopoly/{id}/replay", h.replay)

	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		agent := auth.RequireScope(auth.ScopeAgent)
		r.With(agent).Post("/v1/monopoly/lobby/create", h.create)
		r.With(agent).Post("/v1/monopoly/pushplay", h.pushplay)
		r.With(agent).Get("/v1/monopoly/{id}/state", h.state)
		r.With(agent).Post("/v1/monopoly/{id}/action", h.action)
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
			if fr.seq <= lastSeq {
				continue
			}
			if _, err := w.Write(fr.data); err != nil {
				return
			}
			lastSeq = fr.seq
			_ = rc.Flush()
		case <-ticker.C:
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
	events, err := h.svc.Replay(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"events": events})
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		EntryFee int64 `json:"entry_fee"`
		Players  int   `json:"players"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	id, err := h.svc.CreateTable(r.Context(), p.AgentPublicID, p.UserPublicID, in.EntryFee, in.Players)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	h.hub.RegisterMatch(id)
	httpx.JSON(w, http.StatusCreated, map[string]any{"match_id": id})
}

func (h *Handler) state(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	view, err := h.svc.State(r.Context(), chi.URLParam(r, "id"), p.AgentPublicID)
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
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	act := mono.Action{Kind: in.Action, Property: in.Property, Amount: in.Amount, Trade: in.Trade}
	view, err := h.svc.Act(r.Context(), p.AgentPublicID, chi.URLParam(r, "id"), act)
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
