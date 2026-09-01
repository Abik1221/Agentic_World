package store

import (
	"context"

	"github.com/agent-arena/arena/internal/skill"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SkillRepo reads unscored decisions and writes back their decision-quality verdicts.
//
// Scoring is an OFFLINE batch, deliberately. It is a pure function of columns already on
// the row (input_json, action), so it needs no live match, adds nothing to the latency of
// a turn, and touches no money path. That also makes it re-runnable: when the scorer
// improves, bump skill.ScorerVersion and the same worker back-fills the whole history
// with no migration and no replay.
type SkillRepo struct{ db *pgxpool.Pool }

func NewSkillRepo(db *pgxpool.Pool) *SkillRepo { return &SkillRepo{db: db} }

// NextUnscored returns up to limit decisions that have a view to score but no score at
// the current scorer version.
//
// Ordered by (match_id, agent_id, seq) so a run is reproducible and so decisions from one
// match are scored together — which matters because a partially-scored match would report
// a misleading per-match summary if anything read it mid-batch.
// The two halves of "not scored at the current version", named so the repo and its test
// read the SAME strings. Pinned as constants rather than inlined because their correctness
// is a relationship BETWEEN them — together they must cover the eligible set exactly once —
// and a test that restated them in its own words would keep passing while the code drifted.
//
// scorerPhaseNeverScored has no score at all; scorerPhaseStaleScore has one that is behind.
// The IS NOT NULL in the second is what makes them disjoint, not merely ordered.
const (
	scorerPhaseNeverScored = `d.skill_scorer_version IS NULL`
	scorerPhaseStaleScore  = `d.skill_scorer_version IS NOT NULL AND d.skill_scorer_version < $1`
)

func (r *SkillRepo) NextUnscored(ctx context.Context, limit int) ([]skill.Unscored, error) {
	if limit <= 0 {
		limit = 500
	}

	// TWO QUERIES, NOT ONE, and the reason is which index each can use.
	//
	// This was originally one query asking for
	//
	//	skill_scorer_version IS NULL OR skill_scorer_version < $1
	//
	// and it cost 93 seconds and 4.7 GB of reads per batch — 188 GB across 40 batches on
	// the lab database. The interesting part is WHY, because three plausible explanations
	// were wrong and each was checked:
	//
	//   - not a sequential scan: the planner used an index throughout;
	//   - not TOAST: input_json averages 2 KB, so 500 rows is about a megabyte of payload;
	//   - not bloat: vacuuming from 594,089 dead tuples down to 570 changed nothing.
	//
	// It was the ORDER BY. The planner satisfied "ORDER BY match_id, agent_id, seq LIMIT
	// 500" with the primary key, which is exactly those columns, and then filtered on the
	// heap. But match_id order is roughly age order, and the OLDEST decisions are the ones
	// already scored — so every batch walked through hundreds of thousands of scored rows
	// before reaching 500 unscored ones, and did it again on the next batch. Cost grows
	// with the size of the scored prefix, which is to say it gets worse forever.
	//
	// A partial index on the unscored rows does not contain the scored prefix at all, so it
	// starts where the work is: the same scan reads 6,372 buffers instead of 560,000. But a
	// partial index can only be used when the query's predicate matches it, and the OR
	// version never could. Hence two predicates, each matching an index that exists:
	//
	//	never scored          -> idx_agent_match_decisions_unscored
	//	scored, older version -> idx_decisions_stale_score        (migration 0096)
	//
	// Never-scored first because that is both the common case and the right priority: a
	// decision with no score at all is a gap in the record, while one scored at an older
	// version already has a usable answer. The second query only runs when the first comes
	// up short, so after a ScorerVersion bump the backlog drains and then the rescore
	// begins — and in steady state the second query is not run at all.
	//
	// Semantics are unchanged from the single-OR version: the same rows are eligible, and
	// each phase is still ordered by (match_id, agent_id, seq) so a run is reproducible and
	// one match's decisions stay together.
	out, err := r.unscoredBatch(ctx, scorerPhaseNeverScored, limit, false)
	if err != nil {
		return nil, err
	}
	if len(out) >= limit {
		return out, nil
	}

	// Top up with rows carrying an OUTDATED score. Bounded by what the first query left
	// unfilled so a batch never exceeds the caller's limit.
	stale, err := r.unscoredBatch(ctx, scorerPhaseStaleScore, limit-len(out), true)
	if err != nil {
		return nil, err
	}
	return append(out, stale...), nil
}

// unscoredBatch runs one phase of NextUnscored.
//
// The predicate is interpolated, never the values: `cond` comes from the two constants
// above and nothing reaches it from a request. `useVersion` says whether the condition
// references $1, because a query that declares a parameter it does not use is an error.
func (r *SkillRepo) unscoredBatch(ctx context.Context, cond string, limit int, useVersion bool) ([]skill.Unscored, error) {
	if limit <= 0 {
		return nil, nil
	}
	sql := `SELECT d.match_id, d.agent_id, d.seq, m.game, d.action, d.input_json
		   FROM agent_match_decisions d
		   JOIN matches m ON m.public_id = d.match_id
		  WHERE d.input_json IS NOT NULL AND (` + cond + `)
		  ORDER BY d.match_id, d.agent_id, d.seq
		  LIMIT `
	var rows pgx.Rows
	var err error
	if useVersion {
		rows, err = r.db.Query(ctx, sql+`$2`, skill.ScorerVersion, limit)
	} else {
		rows, err = r.db.Query(ctx, sql+`$1`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []skill.Unscored
	for rows.Next() {
		var d skill.Unscored
		if err := rows.Scan(&d.MatchID, &d.AgentID, &d.Seq, &d.Game, &d.Action, &d.Input); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SaveScores writes a batch of verdicts.
//
// A nil Regret is written as SQL NULL and the row is still stamped with the scorer
// version — see skill.Worker.Once. "We looked and could not score this" must not read as
// "scored zero", which would drag an agent's mean down for the platform's own gap.
func (r *SkillRepo) SaveScores(ctx context.Context, scores []skill.Scored) error {
	if len(scores) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, s := range scores {
		batch.Queue(
			`UPDATE agent_match_decisions
			    SET skill_regret = $4, skill_best = $5, skill_scorer_version = $6
			  WHERE match_id = $1 AND agent_id = $2 AND seq = $3`,
			s.MatchID, s.AgentID, s.Seq, s.Regret, s.Best, skill.ScorerVersion)
	}
	br := r.db.SendBatch(ctx, batch)
	defer br.Close()
	for range scores {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}
