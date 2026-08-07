package modelboard

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Service serves a fitted board from a cached snapshot, and refreshes it in the background.
//
// # Why this is never computed on a request
//
// One fit is a regularized optimization plus a thousand bootstrap replicates, each of which is
// another full optimization. That is seconds of CPU on real data and grows with the season. Doing
// it per request would make the board its own denial of service — and worse, two readers hitting
// it at once would each pay the cost to compute the same answer.
//
// The board also does not change quickly: it moves when matches FINISH, which is minutes apart at
// best. A snapshot refreshed on an interval is the honest shape for that, and it has a property
// on-demand computation does not — every reader in a refresh window sees the SAME board, so two
// people comparing screenshots are not looking at two different fits.
//
// # Why a stale board is served rather than an error
//
// If a refresh fails the previous snapshot keeps being served, with its age attached. A board that
// is twenty minutes old is almost always more useful than a 503, and the age is published so a
// reader can judge that for themselves rather than being told a stale number is current.
type Service struct {
	seats  SeatSource
	log    *slog.Logger
	build  BuildConfig
	fit    Config
	window time.Duration

	mu       sync.RWMutex
	snapshot *Snapshot
}

// SeatSource reads the seats a board is fitted from. Satisfied by *store.ModelBoardRepo.
type SeatSource interface {
	Seats(ctx context.Context, game string, start, end time.Time) ([]Seat, error)
}

// Snapshot is one computed board plus the provenance a reader needs to judge it.
type Snapshot struct {
	Board Board `json:"board"`
	// ComputedAt is when the fit ran. Published because a leaderboard without a timestamp invites
	// the assumption that it is live, and this one deliberately is not.
	ComputedAt time.Time `json:"computed_at"`
	// WindowDays is how far back the matches were drawn from, so a reader knows whether they are
	// looking at a season or a week.
	WindowDays int `json:"window_days"`
	// TookMS is how long the fit took. Operational, but published: a board whose fit time is
	// climbing is one whose refresh interval will eventually stop being honest.
	TookMS int64 `json:"took_ms"`
}

// NewService constructs the board service. window is how far back matches are drawn from.
func NewService(seats SeatSource, window time.Duration, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		seats: seats, log: log,
		build: DefaultBuildConfig(), fit: DefaultConfig(),
		window: window,
	}
}

// Snapshot returns the current board, or nil when none has been computed yet.
//
// nil rather than an empty board on purpose: "we have not fitted one yet" and "we fitted one and
// no model qualified" are different states, and the second is a real finding about the platform
// that must not be manufactured by the first.
func (s *Service) Snapshot() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

// Refresh recomputes the board and replaces the snapshot.
//
// On failure the previous snapshot is LEFT IN PLACE. A refresh that cannot read the database
// should not also destroy the last good answer — that would turn a transient outage into an empty
// leaderboard, which reads to a developer as "my model was removed".
func (s *Service) Refresh(ctx context.Context) error {
	start := time.Now()
	from := start.Add(-s.window)
	seats, err := s.seats.Seats(ctx, "", from, start.Add(time.Hour))
	if err != nil {
		return err
	}
	board := Build(seats, s.build, s.fit)
	snap := &Snapshot{
		Board:      board,
		ComputedAt: start,
		WindowDays: int(s.window.Hours() / 24),
		TookMS:     time.Since(start).Milliseconds(),
	}
	s.mu.Lock()
	s.snapshot = snap
	s.mu.Unlock()
	s.log.Info("model board refreshed", "summary", board.Summary(), "took_ms", snap.TookMS)
	return nil
}

// Worker refreshes the board on an interval.
type Worker struct {
	svc      *Service
	interval time.Duration
	log      *slog.Logger
}

func NewWorker(svc *Service, interval time.Duration, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.Default()
	}
	return &Worker{svc: svc, interval: interval, log: log}
}

// Run refreshes until ctx is cancelled, once immediately on start.
//
// The immediate run matters: without it the board is empty for a whole interval after a deploy,
// and an empty board is indistinguishable from "no model qualified" — a claim we would be making
// by accident.
func (w *Worker) Run(ctx context.Context) {
	w.log.Info("model board worker started", "interval", w.interval.String())
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		if err := w.svc.Refresh(ctx); err != nil {
			// Logged, not fatal: the previous snapshot is still being served, and a board that
			// keeps working through a database blip is the point of having a snapshot at all.
			w.log.Error("model board refresh failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
