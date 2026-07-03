// Package mafia streams a social-deduction match to spectators over SSE. It is a
// second game alongside Goofspiel, played entirely by AI agents while humans
// watch. Until a real LLM-driven engine exists, a per-match Director replays a
// canonical scripted match on a loop, so the spectator console connects to a
// genuine live stream (Last-Event-ID resume, mid-match backlog, heartbeats) with
// the exact event contract a real engine will later emit.
//
// Fan-out mirrors the spectator Hub: non-blocking and drop-slow, so a stalled
// watcher can never delay the match clock.
package mafia

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/prometheus/client_golang/prometheus"
)

// ErrTooManyWatchers is returned when this instance's per-match watcher cap is
// reached; the load balancer routes additional spectators to other instances.
var ErrTooManyWatchers = httpx.NewError(http.StatusServiceUnavailable, "watchers_full", "This match has too many watchers on this node; retry.")

// frame is one pre-encoded SSE event tagged with its sequence number so a
// resuming client can dedup against its backlog.
type frame struct {
	seq  int
	data []byte
}

// sub is a single watcher's buffered mailbox. Closing dead signals the handler
// goroutine to disconnect (used when the buffer overflows = slow consumer).
type sub struct {
	ch   chan frame
	dead chan struct{}
	once sync.Once
}

func (s *sub) kill() { s.once.Do(func() { close(s.dead) }) }

// EventBacklog loads persisted events for SSE resume on real matches.
type EventBacklog interface {
	LoadEvents(ctx context.Context, matchPublicID string, afterSeq int) ([]mf.Event, error)
}

// Hub fans match events out to per-match subscriber sets and owns the Directors
// that generate those events. Safe for concurrent use.
type Hub struct {
	mu          sync.RWMutex
	subs        map[string]map[*sub]struct{}
	directors   map[string]*director
	realMatches map[string]struct{}

	eventLog EventBacklog
	log     *slog.Logger
	m       *metrics
	bufSize int
	maxPerMatch int
}

// NewHub builds the broadcast hub and seeds the always-on demo table.
func NewHub(backlog EventBacklog, log *slog.Logger, reg *prometheus.Registry) *Hub {
	h := &Hub{
		subs:        map[string]map[*sub]struct{}{},
		directors:   map[string]*director{},
		realMatches: map[string]struct{}{},
		eventLog:    backlog,
		log:         log,
		m:           newMetrics(reg),
		bufSize:     64,
		maxPerMatch: 1000,
	}
	h.directors[DemoMatchID] = newDirector(DemoMatchID, "Mafia AI Arena · Table 01", demoScript, h,
		2200*time.Millisecond, 7*time.Second)
	return h
}

// RegisterMatch marks a DB-backed table as watchable over SSE.
func (h *Hub) RegisterMatch(matchID string) {
	h.mu.Lock()
	h.realMatches[matchID] = struct{}{}
	h.mu.Unlock()
}

// Broadcast fans engine events to live spectators. Implements mafia.Broadcaster.
//
// Live spectators only ever receive the redacted (public) view: night secrets —
// kill targets, investigation findings, doctor/sheriff actions and the acting
// seats' roles — are stripped here so hidden information never leaves the server
// mid-match. The full log is still persisted for the post-match replay.
func (h *Hub) Broadcast(matchID string, events []mf.Event) {
	if len(events) == 0 {
		return
	}
	h.RegisterMatch(matchID)
	public := mf.RedactLog(events)
	if len(public) == 0 {
		return
	}
	frames := make([]frame, 0, len(public))
	for _, ev := range public {
		frames = append(frames, EncodeEvent(ev))
	}
	h.m.broadcastEvents.Add(float64(len(frames)))

	h.mu.RLock()
	targets := make([]*sub, 0, len(h.subs[matchID]))
	for s := range h.subs[matchID] {
		targets = append(targets, s)
	}
	h.mu.RUnlock()

	for _, s := range targets {
		for _, fr := range frames {
			select {
			case s.ch <- fr:
			case <-s.dead:
				return
			default:
				h.m.droppedSlow.Inc()
				s.kill()
				return
			}
		}
	}
}

// Run starts every seeded Director and blocks until the context is cancelled.
// Intended to be launched in its own goroutine from the composition root.
func (h *Hub) Run(ctx context.Context) {
	h.mu.RLock()
	ds := make([]*director, 0, len(h.directors))
	for _, d := range h.directors {
		ds = append(ds, d)
	}
	h.mu.RUnlock()
	for _, d := range ds {
		go d.loop(ctx)
	}
	<-ctx.Done()
}

// broadcast pushes one frame to every current watcher of matchID. It NEVER
// blocks: a subscriber whose buffer is full is dropped (drop-slow).
func (h *Hub) broadcast(matchID string, fr frame) {
	h.m.broadcastEvents.Inc()
	h.mu.RLock()
	targets := make([]*sub, 0, len(h.subs[matchID]))
	for s := range h.subs[matchID] {
		targets = append(targets, s)
	}
	h.mu.RUnlock()

	for _, s := range targets {
		select {
		case s.ch <- fr:
		case <-s.dead:
		default:
			h.m.droppedSlow.Inc()
			s.kill()
		}
	}
}

// Subscribe registers a watcher for a match. ErrNotFound for an unknown match,
// ErrTooManyWatchers when this instance's per-match cap is hit.
func (h *Hub) Subscribe(matchID string) (*sub, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.directors[matchID] == nil {
		if _, ok := h.realMatches[matchID]; !ok {
			return nil, httpx.ErrNotFound
		}
	}
	set := h.subs[matchID]
	if set == nil {
		set = map[*sub]struct{}{}
		h.subs[matchID] = set
	}
	if len(set) >= h.maxPerMatch {
		return nil, ErrTooManyWatchers
	}
	s := &sub{ch: make(chan frame, h.bufSize), dead: make(chan struct{})}
	set[s] = struct{}{}
	h.m.subscribers.Inc()
	return s, nil
}

// Unsubscribe removes a watcher and releases it.
func (h *Hub) Unsubscribe(matchID string, s *sub) {
	h.mu.Lock()
	if set := h.subs[matchID]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(h.subs, matchID)
		}
	}
	h.mu.Unlock()
	h.m.subscribers.Dec()
	s.kill()
}

// backlog returns frames after lastSeq from the demo director or persisted log.
func (h *Hub) backlog(ctx context.Context, matchID string, lastSeq int) []frame {
	h.mu.RLock()
	d := h.directors[matchID]
	h.mu.RUnlock()
	if d != nil {
		return d.backlog(lastSeq)
	}
	if h.eventLog == nil {
		return nil
	}
	events, err := h.eventLog.LoadEvents(ctx, matchID, lastSeq)
	if err != nil {
		return nil
	}
	// A resuming watcher is a live spectator: redact night secrets exactly like
	// the live broadcast. Full detail is only available post-match via /replay.
	public := mf.RedactLog(events)
	out := make([]frame, 0, len(public))
	for _, ev := range public {
		out = append(out, EncodeEvent(ev))
	}
	return out
}

func (h *Hub) watchers(matchID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[matchID])
}

// liveMatches returns demo tables plus optional DB rows supplied by the caller.
func (h *Hub) liveMatches(extra []LiveMatch) []LiveMatch {
	h.mu.RLock()
	ds := make([]*director, 0, len(h.directors))
	for _, d := range h.directors {
		ds = append(ds, d)
	}
	h.mu.RUnlock()

	out := make([]LiveMatch, 0, len(ds)+len(extra))
	for _, d := range ds {
		st := d.status()
		out = append(out, LiveMatch{
			MatchID:  d.matchID,
			Title:    d.title,
			Agents:   rosterNames,
			Players:  rosterSize,
			Alive:    st.alive,
			Day:      st.day,
			Phase:    st.phase,
			Winner:   st.winner,
			Watchers: h.watchers(d.matchID),
		})
	}
	for i := range extra {
		extra[i].Watchers = h.watchers(extra[i].MatchID)
		out = append(out, extra[i])
	}
	return out
}

// LiveMatch is one row of the public Mafia arena list.
type LiveMatch struct {
	MatchID  string   `json:"match_id"`
	Title    string   `json:"title"`
	Agents   []string `json:"agents"`
	Players  int      `json:"players"`
	Alive    int      `json:"alive"`
	Day      int      `json:"day"`
	Phase    string   `json:"phase"`
	Winner   string   `json:"winner,omitempty"`
	Watchers int      `json:"watchers"`
}

// ── metrics ──────────────────────────────────────────────────────────────────

type metrics struct {
	subscribers     prometheus.Gauge
	droppedSlow     prometheus.Counter
	broadcastEvents prometheus.Counter
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		subscribers: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mafia_sse_subscribers", Help: "Current Mafia SSE spectator connections on this instance.",
		}),
		droppedSlow: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mafia_sse_dropped_slow_total", Help: "Mafia spectators dropped for being too slow to read.",
		}),
		broadcastEvents: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mafia_sse_broadcast_events_total", Help: "Mafia events fanned out to spectators.",
		}),
	}
	reg.MustRegister(m.subscribers, m.droppedSlow, m.broadcastEvents)
	return m
}
