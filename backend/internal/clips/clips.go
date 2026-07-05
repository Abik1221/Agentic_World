package clips

import (
	"context"
	"log/slog"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

// Config tunes the background generation pool.
type Config struct {
	Workers   int
	QueueSize int
}

// Service detects dramatic moments and renders clip assets on a bounded worker
// pool. Enqueue is non-blocking; if the queue is saturated the match id is dropped
// (logged + counted) rather than ever blocking match finalize.
type Service struct {
	repo   Repo
	events EventLog
	gen    Generator
	log    *slog.Logger
	m      *metrics
	queue  chan string
	cfg    Config
}

func New(repo Repo, events EventLog, gen Generator, cfg Config, log *slog.Logger, reg *prometheus.Registry) *Service {
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 256
	}
	return &Service{
		repo: repo, events: events, gen: gen, log: log, m: newMetrics(reg),
		queue: make(chan string, cfg.QueueSize), cfg: cfg,
	}
}

// Enqueue schedules clip processing for a finished match. Non-blocking.
func (s *Service) Enqueue(matchPublicID string) {
	select {
	case s.queue <- matchPublicID:
	default:
		s.m.dropped.Inc()
		s.log.Warn("clip queue full; dropping match (reconcile can re-derive later)", "match", matchPublicID)
	}
}

// Run starts the worker pool and blocks until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	s.log.Info("clip workers started", "workers", s.cfg.Workers)
	done := make(chan struct{})
	for i := 0; i < s.cfg.Workers; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					done <- struct{}{}
					return
				case id := <-s.queue:
					s.process(ctx, id)
				}
			}
		}()
	}
	for i := 0; i < s.cfg.Workers; i++ {
		<-done
	}
	s.log.Info("clip workers stopped")
}

func (s *Service) process(ctx context.Context, matchPublicID string) {
	events, err := s.events.LoadEvents(ctx, matchPublicID)
	if err != nil {
		s.log.Error("clip: load events failed", "match", matchPublicID, "error", err)
		return
	}
	triggers := Detect(events)
	if len(triggers) == 0 {
		return
	}

	in := make([]NewClip, len(triggers))
	for i, t := range triggers {
		in[i] = NewClip{PublicID: platform.NewID("clip"), Trigger: t.Kind, RoundSeq: t.RoundSeq}
	}
	created, err := s.repo.CreateClips(ctx, matchPublicID, in)
	if err != nil {
		s.log.Error("clip: create rows failed", "match", matchPublicID, "error", err)
		return
	}

	for _, c := range created {
		s.m.created.WithLabelValues(c.Trigger).Inc()
		s.renderWithRetry(ctx, matchPublicID, c)
	}
}

// renderWithRetry generates the asset (a few attempts) and records its URL.
func (s *Service) renderWithRetry(ctx context.Context, matchPublicID string, c Created) {
	meta := ClipMeta{ClipPublicID: c.PublicID, MatchPublicID: matchPublicID, Trigger: c.Trigger, RoundSeq: c.RoundSeq}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		url, err := s.gen.Generate(ctx, meta)
		if err == nil {
			if err := s.repo.SetAsset(ctx, c.PublicID, url); err != nil {
				s.log.Error("clip: set asset failed", "clip", c.PublicID, "error", err)
			}
			return
		}
		lastErr = err
	}
	s.m.genFailures.Inc()
	s.log.Error("clip: asset generation failed after retries", "clip", c.PublicID, "error", lastErr)
}

// Trending returns ready clips ranked by shares then recency.
func (s *Service) Trending(ctx context.Context, limit, offset int) ([]ClipView, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.Trending(ctx, limit, offset)
}

// ── metrics ──────────────────────────────────────────────────────────────────

type metrics struct {
	created     *prometheus.CounterVec
	dropped     prometheus.Counter
	genFailures prometheus.Counter
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		created: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "clips_created_total", Help: "Clips created, by trigger.",
		}, []string{"trigger"}),
		dropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "clip_enqueue_dropped_total", Help: "Match clip jobs dropped because the queue was full.",
		}),
		genFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "clip_gen_failures_total", Help: "Clip asset generations that failed after retries.",
		}),
	}
	reg.MustRegister(m.created, m.dropped, m.genFailures)
	return m
}
