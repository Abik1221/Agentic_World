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

	history HistoryWriter

	// board names which series this instance owns. Two instances run the SAME algorithm over
	// different matches — the developer board and the platform harness — and the name is what
	// keeps their histories from overwriting each other.
	board string

	// publishableHosts gates model ATTRIBUTION — see llmgw.PublishableUpstreamHosts. Empty
	// means nothing is attributable, which is the correct failure: a board that cannot
	// establish where a call went should show unattributed seats rather than guess.
	publishableHosts []string

	mu       sync.RWMutex
	snapshot *Snapshot
}

// SetHistoryWriter attaches per-day history persistence. Optional.
func (s *Service) SetHistoryWriter(h HistoryWriter) { s.history = h }

// SetBoard names the series this instance writes. Required before history is recorded;
// see RecordBoardHistory, which refuses an empty name rather than defaulting to one.
func (s *Service) SetBoard(name string) { s.board = name }

// SetPublishableHosts declares which upstreams may be attributed to a model.
//
// A setter rather than a constructor argument so an existing deployment keeps compiling,
// but note what the zero value means: NO host is publishable, so every seat comes back
// unattributed and the board is empty. That is deliberate. The alternative default —
// publish everything — is how a lab stand-in ends up ranked as a model, which is the exact
// failure this whole path exists to prevent. main wires it from llmgw's default set.
func (s *Service) SetPublishableHosts(hosts []string) { s.publishableHosts = hosts }

// SeatSource reads the seats a board is fitted from. Satisfied by *store.ModelBoardRepo.
//
// publishableHosts names the upstreams whose responses may be attributed to a model. It is
// threaded through rather than read inside the repo so the rule is visible at the boundary:
// what the board is willing to publish is a policy decision, not a storage detail.
type SeatSource interface {
	// Seats returns the seats in the window that are worth comparing, plus the exclusions the
	// SOURCE itself resolved, keyed as BuildComparisons keys them.
	//
	// The second return exists because the query now filters: it drops seats from games with no
	// pairwise outcome, which on the developer board is 98.8% of them. seats_excluded is
	// published, so those have to be counted somewhere, and the only place that knows how many
	// there were is the layer that removed them. A source that filters nothing returns nil.
	Seats(ctx context.Context, game string, start, end time.Time, publishableHosts []string) ([]Seat, map[string]int, error)
}

// HistoryWriter persists one day's fitted board so a rating can be shown as a series.
//
// Optional: a deployment without it still serves a current board, it just accrues no history.
// Separate from SeatSource because reading and writing fail independently — a history write that
// errors must not cost the reader the board that was just computed.
type HistoryWriter interface {
	RecordBoardHistory(ctx context.Context, board string, day time.Time, windowDays int, ratings []Rating) error
}

// HistoryPoint is one day of one model's series.
//
// Carries the INTERVAL, not only the point estimate: a rating line without its uncertainty invites
// reading a four-point move as a change when the interval is forty points wide. It carries
// separability for the same reason — a rating that rose while separability fell is a statement
// about one developer rather than about the model.
type HistoryPoint struct {
	Day           string  `json:"day"`
	Elo           float64 `json:"elo"`
	EloLow        float64 `json:"elo_low"`
	EloHigh       float64 `json:"elo_high"`
	Rank          int     `json:"rank"`
	RankStability float64 `json:"rank_stability"`
	Comparisons   int     `json:"comparisons"`
	Wins          int     `json:"wins"`
	Losses        int     `json:"losses"`
	Draws         int     `json:"draws"`
	Separability  float64 `json:"separability"`
	Provisional   bool    `json:"provisional"`
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
	seats, preExcluded, err := s.seats.Seats(ctx, "", from, start.Add(time.Hour), s.publishableHosts)
	if err != nil {
		return err
	}
	board := BuildWithExclusions(seats, preExcluded, s.build, s.fit)
	snap := &Snapshot{
		Board:      board,
		ComputedAt: start,
		WindowDays: int(s.window.Hours() / 24),
		TookMS:     time.Since(start).Milliseconds(),
	}
	s.mu.Lock()
	s.snapshot = snap
	s.mu.Unlock()

	// History AFTER the snapshot is published, and never fatal: the board a reader is about to get
	// is already correct, and losing a day of the series is a smaller harm than failing a refresh
	// that succeeded. History is also the one thing that cannot be backfilled, so the failure is
	// logged loudly rather than swallowed.
	if s.history != nil && len(board.Ratings) > 0 {
		if err := s.history.RecordBoardHistory(ctx, s.board, start, snap.WindowDays, board.Ratings); err != nil {
			s.log.Error("model board history not recorded — this day of the series cannot be "+
				"recovered later", "error", err)
		}
	}
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
		started := time.Now()
		err := w.svc.Refresh(ctx)
		took := time.Since(started)

		// A REFRESH THAT OUTLASTS ITS INTERVAL IS NOT A SCHEDULED JOB ANY MORE.
		//
		// This loop is sequential, so it cannot overlap itself — but the ticker always
		// has a tick waiting when the work takes longer than the period, and the effect
		// is a job that runs continuously while still describing itself as "every 10
		// minutes". That is precisely how it hid: the developer board's refresh had grown
		// to ~16.7 minutes against a 10-minute interval and was scanning permanently,
		// 208 GB of reads across four refreshes, and nothing in the logs said so.
		//
		// Reported rather than corrected, deliberately. Silently stretching the interval
		// would hide the growth that caused it, and skipping refreshes would make the
		// board quietly stale; the operator needs to know the window has outgrown its
		// schedule so the query or the interval can be fixed on purpose.
		if took > w.interval {
			w.log.Warn("model board refresh took longer than its interval — it is now running "+
				"continuously rather than on a schedule",
				"took", took.Round(time.Second).String(),
				"interval", w.interval.String())
		} else {
			w.log.Info("model board refreshed", "took", took.Round(time.Millisecond).String())
		}
		if err != nil {
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
