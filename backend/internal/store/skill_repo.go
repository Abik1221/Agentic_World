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
func (r *SkillRepo) NextUnscored(ctx context.Context, limit int) ([]skill.Unscored, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.db.Query(ctx,
		`SELECT d.match_id, d.agent_id, d.seq, m.game, d.action, d.input_json
		   FROM agent_match_decisions d
		   JOIN matches m ON m.public_id = d.match_id
		  WHERE d.input_json IS NOT NULL
		    AND (d.skill_scorer_version IS NULL OR d.skill_scorer_version < $1)
		  ORDER BY d.match_id, d.agent_id, d.seq
		  LIMIT $2`,
		skill.ScorerVersion, limit)
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
