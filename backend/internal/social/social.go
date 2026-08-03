package social

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
)

// Config tunes the notification worker pool.
type Config struct {
	Workers   int
	QueueSize int
}

// Service handles follows and, on match finalize, the notification fan-out to
// owners and followers. Enqueue is non-blocking (drops + counts on overflow), so
// it never delays match finalize.
type Service struct {
	repo  Repo
	log   *slog.Logger
	m     *metrics
	queue chan string
	cfg   Config
	push  Pusher // realtime fan-out alongside the persisted row; nil ⇒ none
}

// Pusher mirrors a freshly written notification onto the recipient's live stream.
// Optional: without it the row is still persisted and still reaches the bell on
// its next poll — this only removes the wait.
type Pusher interface {
	Push(ctx context.Context, userPublicID, kind, ref string, payload []byte)
}

// SetPusher installs the realtime mirror. Optional; see Pusher.
func (s *Service) SetPusher(p Pusher) { s.push = p }

func New(repo Repo, cfg Config, log *slog.Logger, reg *prometheus.Registry) *Service {
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 256
	}
	return &Service{repo: repo, log: log, m: newMetrics(reg), queue: make(chan string, cfg.QueueSize), cfg: cfg}
}

// FollowState is the viewer's relationship to an agent plus the agent's follower
// count. Returned by the read AND by both mutations so the button and the number come
// from one response and cannot drift apart.
type FollowState struct {
	Following bool `json:"following"`
	Followers int  `json:"followers"`
}

// FollowState reads the relationship without changing it. userPublicID may be empty
// (signed out): the count is public, the relationship is then false.
func (s *Service) FollowState(ctx context.Context, userPublicID, agentPublicID string) (FollowState, error) {
	following, followers, err := s.repo.FollowState(ctx, userPublicID, agentPublicID)
	return FollowState{Following: following, Followers: followers}, err
}

// Follow / Unfollow are the user-facing follow operations. Both are IDEMPOTENT and both
// return the resulting state: a double-tap on a phone, or a retry after a dropped
// response, must confirm rather than toggle twice.
func (s *Service) Follow(ctx context.Context, userPublicID, agentPublicID string) (FollowState, error) {
	if err := s.repo.Follow(ctx, userPublicID, agentPublicID); err != nil {
		return FollowState{}, err
	}
	return s.FollowState(ctx, userPublicID, agentPublicID)
}

func (s *Service) Unfollow(ctx context.Context, userPublicID, agentPublicID string) (FollowState, error) {
	if err := s.repo.Unfollow(ctx, userPublicID, agentPublicID); err != nil {
		return FollowState{}, err
	}
	return s.FollowState(ctx, userPublicID, agentPublicID)
}

// Notifications returns a user's recent notification feed (newest first).
func (s *Service) Notifications(ctx context.Context, userPublicID string, limit int) ([]Notification, error) {
	return s.repo.ListNotifications(ctx, userPublicID, limit)
}

// MarkRead marks all of a user's notifications read; returns the count updated.
func (s *Service) MarkRead(ctx context.Context, userPublicID string) (int, error) {
	return s.repo.MarkAllRead(ctx, userPublicID)
}

// Enqueue schedules notification fan-out for a finished match. Non-blocking.
func (s *Service) Enqueue(matchPublicID string) {
	select {
	case s.queue <- matchPublicID:
	default:
		s.m.dropped.Inc()
		s.log.Warn("notification queue full; dropping match", "match", matchPublicID)
	}
}

// Run starts the worker pool and blocks until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	s.log.Info("notification workers started", "workers", s.cfg.Workers)
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
	s.log.Info("notification workers stopped")
}

func (s *Service) process(ctx context.Context, matchPublicID string) {
	parts, err := s.repo.MatchParticipants(ctx, matchPublicID)
	if err != nil {
		s.log.Error("notify: load participants failed", "match", matchPublicID, "error", err)
		return
	}
	for _, p := range parts {
		ref := "match:" + matchPublicID + ":" + p.AgentPublicID
		payload, _ := json.Marshal(map[string]any{
			"match": matchPublicID, "agent": p.AgentPublicID,
			"result": resultOf(p.CoinsDelta), "coins_delta": p.CoinsDelta,
		})

		// Owner gets a match-result notification.
		if ins, err := s.repo.InsertNotification(ctx, p.OwnerPublicID, "match_result", ref, payload); err != nil {
			s.log.Error("notify: owner insert failed", "match", matchPublicID, "error", err)
		} else if ins {
			s.m.sent.Inc()
			// Push ONLY on a genuine insert. The row is idempotent per (recipient,
			// kind, ref), so a re-processed match returns ins=false — pushing there
			// would re-toast a result the user already saw.
			s.pushLive(ctx, p.OwnerPublicID, "match_result", ref, payload)
		}

		// Followers of this agent get an agent-match notification.
		followers, err := s.repo.FollowerUserIDs(ctx, p.AgentPublicID)
		if err != nil {
			s.log.Error("notify: followers load failed", "agent", p.AgentPublicID, "error", err)
			continue
		}
		for _, f := range followers {
			if ins, err := s.repo.InsertNotification(ctx, f, "agent_match", ref, payload); err != nil {
				s.log.Error("notify: follower insert failed", "recipient", f, "error", err)
			} else if ins {
				s.m.sent.Inc()
				s.pushLive(ctx, f, "agent_match", ref, payload)
			}
		}
	}
}

// pushLive mirrors one persisted notification onto the recipient's live stream.
func (s *Service) pushLive(ctx context.Context, userPublicID, kind, ref string, payload []byte) {
	if s.push == nil {
		return
	}
	s.push.Push(ctx, userPublicID, kind, ref, payload)
}

func resultOf(coinsDelta int64) string {
	switch {
	case coinsDelta > 0:
		return "win"
	case coinsDelta < 0:
		return "loss"
	default:
		return "tie"
	}
}

// ── metrics ──────────────────────────────────────────────────────────────────

type metrics struct {
	sent    prometheus.Counter
	dropped prometheus.Counter
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		sent:    prometheus.NewCounter(prometheus.CounterOpts{Name: "notifications_sent_total", Help: "Notifications written."}),
		dropped: prometheus.NewCounter(prometheus.CounterOpts{Name: "notification_enqueue_dropped_total", Help: "Match notification jobs dropped (queue full)."}),
	}
	reg.MustRegister(m.sent, m.dropped)
	return m
}
