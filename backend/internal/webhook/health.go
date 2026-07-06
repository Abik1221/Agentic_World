package webhook

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
)

// HealthTracker is a per-endpoint circuit breaker shared by the Dispatcher (which
// consults it before delivering) and the Monitor (which probes /health and feeds
// it). After FailThreshold consecutive failures an endpoint's circuit OPENS for a
// cooldown that grows with each subsequent failure (capped), during which the
// dispatcher skips it. A single success closes the circuit. Safe for concurrent
// use.
type HealthTracker struct {
	mu    sync.Mutex
	state map[string]*endpointHealth

	failThreshold int
	baseCooldown  time.Duration
	maxCooldown   time.Duration
	now           func() time.Time
}

type endpointHealth struct {
	consecutiveFailures int
	openUntil           time.Time
	healthy             bool
	lastLatencyMs       int
	lastError           string
	lastChecked         time.Time
}

// HealthConfig tunes the breaker. Zero values fall back to defaults.
type HealthConfig struct {
	FailThreshold int           // consecutive failures before opening (default 3)
	BaseCooldown  time.Duration // first open duration; grows per extra failure (default 15s)
	MaxCooldown   time.Duration // cooldown ceiling (default 5m)
}

// NewHealthTracker builds a tracker.
func NewHealthTracker(cfg HealthConfig) *HealthTracker {
	if cfg.FailThreshold <= 0 {
		cfg.FailThreshold = 3
	}
	if cfg.BaseCooldown <= 0 {
		cfg.BaseCooldown = 15 * time.Second
	}
	if cfg.MaxCooldown <= 0 {
		cfg.MaxCooldown = 5 * time.Minute
	}
	return &HealthTracker{
		state:         make(map[string]*endpointHealth),
		failThreshold: cfg.FailThreshold,
		baseCooldown:  cfg.BaseCooldown,
		maxCooldown:   cfg.MaxCooldown,
		now:           time.Now,
	}
}

// Cooldown is the base cooldown, used by the dispatcher as the defer interval when
// a circuit is open.
func (h *HealthTracker) Cooldown() time.Duration { return h.baseCooldown }

// Allow reports whether delivery to url is currently permitted (circuit closed or
// cooldown elapsed). Unknown endpoints are allowed (optimistic first contact).
func (h *HealthTracker) Allow(url string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.state[url]
	if s == nil {
		return true
	}
	return !h.now().Before(s.openUntil)
}

// RecordSuccess closes the circuit and clears the failure streak.
func (h *HealthTracker) RecordSuccess(url string, latencyMs int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.get(url)
	s.consecutiveFailures = 0
	s.openUntil = time.Time{}
	s.healthy = true
	s.lastLatencyMs = latencyMs
	s.lastError = ""
	s.lastChecked = h.now()
}

// RecordFailure increments the streak and opens the circuit once the threshold is
// crossed, for a cooldown that grows with the streak (capped).
func (h *HealthTracker) RecordFailure(url, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.get(url)
	s.consecutiveFailures++
	s.healthy = false
	s.lastError = reason
	s.lastChecked = h.now()
	if s.consecutiveFailures >= h.failThreshold {
		cd := h.baseCooldown
		for i := h.failThreshold; i < s.consecutiveFailures && cd < h.maxCooldown; i++ {
			cd *= 2
		}
		if cd > h.maxCooldown {
			cd = h.maxCooldown
		}
		s.openUntil = h.now().Add(cd)
	}
}

func (h *HealthTracker) get(url string) *endpointHealth {
	s := h.state[url]
	if s == nil {
		s = &endpointHealth{healthy: true}
		h.state[url] = s
	}
	return s
}

// EndpointHealth is a read-only snapshot for observability (dashboards/logs).
type EndpointHealth struct {
	URL                 string    `json:"url"`
	Healthy             bool      `json:"healthy"`
	CircuitOpen         bool      `json:"circuit_open"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LastLatencyMs       int       `json:"last_latency_ms"`
	LastError           string    `json:"last_error,omitempty"`
	LastChecked         time.Time `json:"last_checked,omitempty"`
}

// Snapshot returns the current health of every tracked endpoint, URL-sorted.
func (h *HealthTracker) Snapshot() []EndpointHealth {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	out := make([]EndpointHealth, 0, len(h.state))
	for url, s := range h.state {
		out = append(out, EndpointHealth{
			URL:                 url,
			Healthy:             s.healthy,
			CircuitOpen:         now.Before(s.openUntil),
			ConsecutiveFailures: s.consecutiveFailures,
			LastLatencyMs:       s.lastLatencyMs,
			LastError:           s.lastError,
			LastChecked:         s.lastChecked,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// --- continuous health monitor ------------------------------------------------

// MonitorResolver enumerates active agents and resolves each to a push target.
type MonitorResolver interface {
	ActiveAgentIDs(ctx context.Context) ([]string, error)
	PlayTarget(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error)
}

// Prober probes an endpoint's liveness. *agentclient.Client satisfies it.
type Prober interface {
	Health(ctx context.Context, t agentclient.Target) (agentclient.HealthResult, error)
}

// Monitor periodically probes every active agent's /health and feeds the tracker,
// so the dispatcher's skip decisions reflect live endpoint state rather than only
// in-band delivery failures. This is the "continuous health monitoring" half of
// the story; the circuit breaker is the "skip unhealthy endpoints" half.
type Monitor struct {
	resolver MonitorResolver
	prober   Prober
	health   *HealthTracker
	log      *slog.Logger
	interval time.Duration
	workers  int
}

// NewMonitor wires a monitor. interval<=0 defaults to 30s.
func NewMonitor(resolver MonitorResolver, prober Prober, health *HealthTracker, log *slog.Logger, interval time.Duration) *Monitor {
	if log == nil {
		log = slog.Default()
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &Monitor{resolver: resolver, prober: prober, health: health, log: log, interval: interval, workers: 8}
}

// Run probes on a ticker until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	m.log.Info("webhook health monitor started", "interval", m.interval.String())
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.tick(ctx); err != nil {
				m.log.Error("health monitor tick failed", "error", err)
			}
		}
	}
}

func (m *Monitor) tick(ctx context.Context) error {
	ids, err := m.resolver.ActiveAgentIDs(ctx)
	if err != nil {
		return err
	}
	sem := make(chan struct{}, m.workers)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			m.probe(ctx, id)
		}(id)
	}
	wg.Wait()
	return nil
}

func (m *Monitor) probe(ctx context.Context, agentID string) {
	target, found, err := m.resolver.PlayTarget(ctx, agentID)
	if err != nil || !found || target.EndpointURL == "" {
		return
	}
	res, err := m.prober.Health(ctx, target)
	if err != nil || !res.OK {
		reason := "unhealthy"
		if err != nil {
			reason = err.Error()
		} else if res.Err != "" {
			reason = res.Err
		}
		m.health.RecordFailure(target.EndpointURL, reason)
		return
	}
	m.health.RecordSuccess(target.EndpointURL, res.LatencyMs)
}
