package skill

import (
	"context"
	"log/slog"
	"time"
)

// The scoring worker.
//
// Scoring runs OFF the match path on purpose. It is a pure function of data already
// persisted, so doing it inline would add latency to a turn and put a CPU-heavy
// computation (regret matching over a payoff matrix) next to a money path, for no benefit
// — the answer is identical whenever it is computed.
//
// Running it as a batch buys three things that inline scoring cannot:
//
//   - BACKFILL. Every decision ever recorded with a view can be scored, so the metric
//     arrives with history behind it instead of starting from empty.
//   - RECOMPUTE. Bump ScorerVersion and the same loop rescores everything that is behind.
//     No migration, no replay, no coordination.
//   - ISOLATION. A bug in a scorer cannot wedge a match, delay a settlement, or cost a
//     developer a turn. The worst it can do is leave scores stale.

// Source supplies unscored work and accepts verdicts. Implemented by store.SkillRepo; an
// interface so the worker is testable without a database.
type Source interface {
	NextUnscored(ctx context.Context, limit int) ([]Unscored, error)
	SaveScores(ctx context.Context, scores []Scored) error
}

// Unscored is one decision awaiting a verdict.
type Unscored struct {
	MatchID string
	AgentID int64
	Seq     int
	Game    string
	Action  string
	Input   []byte
}

// Scored is one verdict. Regret is nil when the decision could not be scored.
type Scored struct {
	MatchID string
	AgentID int64
	Seq     int
	Regret  *float64
	Best    string
}

// WorkerConfig tunes the loop.
type WorkerConfig struct {
	// Batch is how many decisions to score per pass.
	Batch int
	// Interval is how long to wait after an EMPTY pass. A pass that found work loops
	// straight on, so a large backfill drains at full speed instead of trickling at one
	// batch per interval.
	Interval time.Duration
}

func (c WorkerConfig) withDefaults() WorkerConfig {
	if c.Batch <= 0 {
		c.Batch = 500
	}
	if c.Interval <= 0 {
		c.Interval = 30 * time.Second
	}
	return c
}

// Worker scores decisions in the background.
type Worker struct {
	src Source
	cfg WorkerConfig
	log *slog.Logger
}

func NewWorker(src Source, cfg WorkerConfig, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.Default()
	}
	return &Worker{src: src, cfg: cfg.withDefaults(), log: log}
}

// Run scores until the context is cancelled.
//
// Returns no error, matching the platform's other background workers: there is no error
// here a supervisor could act on, and a scoring outage must never take the server down.
// A stale leaderboard is recoverable by simply running again; a crash-looping worker is
// an operator's evening.
//
// Safe to run on several instances. The batch query and the write are both keyed on
// (match_id, agent_id, seq), so two workers that pick up the same row write the same
// deterministic verdict to it. Scoring being a pure function is what makes concurrency a
// non-problem rather than something to lock around.
func (w *Worker) Run(ctx context.Context) {
	for {
		n, err := w.Once(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.log.Warn("skill: scoring pass failed; retrying", "error", err)
		}
		// Work remaining ⇒ keep going immediately; the interval is for an idle platform.
		delay := time.Duration(0)
		if n == 0 || err != nil {
			delay = w.cfg.Interval
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// Once scores a single batch and returns how many decisions it scored.
func (w *Worker) Once(ctx context.Context) (int, error) {
	batch, err := w.src.NextUnscored(ctx, w.cfg.Batch)
	if err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		return 0, nil
	}

	out := make([]Scored, 0, len(batch))
	scored, skipped := 0, 0
	for _, u := range batch {
		s := Scored{MatchID: u.MatchID, AgentID: u.AgentID, Seq: u.Seq}
		if d, ok := ScoreDecision(u.Game, u.Input, u.Action); ok {
			r := d.Regret
			s.Regret, s.Best = &r, d.Best
			scored++
		} else {
			// Stamped with the version but left NULL — see store.ScoredDecision.Regret.
			// Without the stamp the worker would re-read the same unscorable rows on
			// every pass forever and never reach the rest of the backlog.
			skipped++
		}
		out = append(out, s)
	}
	if err := w.src.SaveScores(ctx, out); err != nil {
		return 0, err
	}
	w.log.Info("skill: scored a batch", "scored", scored, "unscorable", skipped, "version", ScorerVersion)
	return scored, nil
}

// ScoreDecision dispatches to the right game's scorer.
//
// An unknown game returns false rather than a zero score. Zero regret means "played the
// best available move", so scoring an arena nobody wrote a scorer for would hand every
// agent in it a perfect record it never earned — worse than having no metric there at all.
// The same applies within a game: Monopoly declines to score trades and forced turns, and
// says so rather than guessing.
func ScoreDecision(game string, input []byte, action string) (Decision, bool) {
	switch game {
	case "goofspiel":
		st, ok := GoofspielStateFromView(input)
		if !ok {
			return Decision{}, false
		}
		card, err := atoiStrict(action)
		if err != nil {
			return Decision{}, false
		}
		return ScoreGoofspielBid(st, card)
	case "monopoly":
		return ScoreMonopolyDecision(input, action)
	default:
		return Decision{}, false
	}
}

// atoiStrict parses an action string with no tolerance for surrounding junk: an action we
// cannot read exactly is one we must not score.
func atoiStrict(s string) (int, error) {
	var n int
	if s == "" {
		return 0, errBadAction
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errBadAction
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

type badAction struct{}

func (badAction) Error() string { return "skill: unparseable action" }

var errBadAction = badAction{}
