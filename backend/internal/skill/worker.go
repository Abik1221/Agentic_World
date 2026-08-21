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

// MafiaSource supplies whole mafia matches. Separate from Source because Mafia's statistic is
// defined on a match-seat rather than on a decision — see mafiamatch.go. Optional: a Source
// that does not implement it simply leaves Mafia unscored, which is the behaviour that shipped
// for as long as nobody noticed, so making it required would break every existing test for a
// capability they do not exercise.
type MafiaSource interface {
	NextUnscoredMafiaMatches(ctx context.Context, version, limit int) ([]MafiaMatchInput, error)
	SaveMafiaMatchScores(ctx context.Context, matchID string, version int, scores []MafiaSeatScore) error
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
		if m, merr := w.OnceMafia(ctx); merr != nil {
			w.log.Warn("skill: mafia scoring pass failed; retrying", "error", merr)
		} else {
			n += m
		}
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

// OnceMafia scores a batch of whole mafia matches and returns how many seats it scored.
//
// A no-op when the source cannot supply matches, so the per-decision loop is unaffected by
// whether this capability is wired.
func (w *Worker) OnceMafia(ctx context.Context) (int, error) {
	src, ok := w.src.(MafiaSource)
	if !ok {
		return 0, nil
	}
	// Deliberately smaller than the decision batch: one match carries a whole event log, and
	// a seat's verdict fans out to every vote it cast.
	matches, err := src.NextUnscoredMafiaMatches(ctx, ScorerVersion, mafiaMatchBatch)
	if err != nil {
		return 0, err
	}
	seats := 0
	for _, m := range matches {
		scores := ScoreMafiaMatch(m)
		if err := src.SaveMafiaMatchScores(ctx, m.MatchID, ScorerVersion, scores); err != nil {
			return seats, err
		}
		seats += len(scores)
	}
	if len(matches) > 0 {
		w.log.Info("skill: scored mafia matches", "matches", len(matches), "seats", seats,
			"version", ScorerVersion)
	}
	return seats, nil
}

const mafiaMatchBatch = 25
