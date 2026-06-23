// Package spectator turns matches into a watchable show. Its Hub implements the
// match.Broadcaster port stubbed in Stage 3: the match worker calls Broadcast
// synchronously under the match lock, so every send here is NON-BLOCKING and
// drop-slow — a stalled spectator is dropped, never allowed to delay match
// adjudication. Streams are read-only and carry only redacted, already-revealed
// event payloads (the engine never puts a sealed card value in an event).
package spectator

import (
	"log/slog"
	"sync"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/prometheus/client_golang/prometheus"
)

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

// Hub fans match events out to per-match subscriber sets. Safe for concurrent use.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[*sub]struct{}

	events       EventLog
	log          *slog.Logger
	m            *metrics
	bufSize      int
	maxPerMatch  int
	defaultRound int // total rounds assumed for commentary (no per-match state)
}

// NewHub builds the broadcast hub. bufSize is the per-subscriber backlog before a
// consumer is judged slow; maxPerMatch caps watchers per match per instance.
func NewHub(events EventLog, defaultRounds int, log *slog.Logger, reg *prometheus.Registry) *Hub {
	return &Hub{
		subs:         map[string]map[*sub]struct{}{},
		events:       events,
		log:          log,
		m:            newMetrics(reg),
		bufSize:      32,
		maxPerMatch:  1000,
		defaultRound: defaultRounds,
	}
}

// Broadcast pushes events to every watcher of matchPublicID. It NEVER blocks: a
// subscriber whose buffer is full is dropped (drop-slow). Implements
// match.Broadcaster.
func (h *Hub) Broadcast(matchPublicID string, events []gs.Event) {
	if len(events) == 0 {
		return
	}
	frames := make([]frame, 0, len(events))
	for _, ev := range events {
		frames = append(frames, h.encodeEvent(ev))
	}
	h.m.broadcastEvents.Add(float64(len(frames)))

	h.mu.RLock()
	targets := make([]*sub, 0, len(h.subs[matchPublicID]))
	for s := range h.subs[matchPublicID] {
		targets = append(targets, s)
	}
	h.mu.RUnlock()

	for _, s := range targets {
	deliver:
		for _, fr := range frames {
			select {
			case s.ch <- fr:
			case <-s.dead:
				break deliver
			default:
				// Buffer full → slow consumer. Drop it; the match worker moves on.
				h.m.droppedSlow.Inc()
				s.kill()
				break deliver
			}
		}
	}
}

// Subscribe registers a new watcher for a match. ErrTooManyWatchers is returned
// when this instance's per-match cap is hit (the load balancer spreads the rest).
func (h *Hub) Subscribe(matchPublicID string) (*sub, error) {
	s := &sub{ch: make(chan frame, h.bufSize), dead: make(chan struct{})}
	h.mu.Lock()
	set := h.subs[matchPublicID]
	if set == nil {
		set = map[*sub]struct{}{}
		h.subs[matchPublicID] = set
	}
	if len(set) >= h.maxPerMatch {
		h.mu.Unlock()
		return nil, ErrTooManyWatchers
	}
	set[s] = struct{}{}
	h.mu.Unlock()
	h.m.subscribers.Inc()
	return s, nil
}

// Unsubscribe removes a watcher and releases it.
func (h *Hub) Unsubscribe(matchPublicID string, s *sub) {
	h.mu.Lock()
	if set := h.subs[matchPublicID]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(h.subs, matchPublicID)
		}
	}
	h.mu.Unlock()
	h.m.subscribers.Dec()
	s.kill()
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
			Name: "sse_subscribers", Help: "Current SSE spectator connections on this instance.",
		}),
		droppedSlow: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "sse_dropped_slow_total", Help: "Spectators dropped for being too slow to read.",
		}),
		broadcastEvents: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "sse_broadcast_events_total", Help: "Events fanned out to spectators.",
		}),
	}
	reg.MustRegister(m.subscribers, m.droppedSlow, m.broadcastEvents)
	return m
}
