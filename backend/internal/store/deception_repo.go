package store

import (
	"context"

	"github.com/agent-arena/arena/internal/deception"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DeceptionRepo reads the ground truth the deception index scores against.
type DeceptionRepo struct{ db *pgxpool.Pool }

func NewDeceptionRepo(db *pgxpool.Pool) *DeceptionRepo { return &DeceptionRepo{db: db} }

// SeatScores returns per-seat vote records for finished Mafia matches.
//
// # Where each half comes from
//
// TRUTH is matches.state->'roles': the assignment the engine made, which no seat but the mafia
// team could see. PUBLIC BEHAVIOUR is the vote map carried in a decision's recorded view — the
// engine's own tally of who voted for whom.
//
// Both are engine facts. Nothing in this query touches a message, and that is deliberate: a
// score derived from chat would be a sentiment classifier, unreproducible across model versions
// and gameable by anyone who read the scoring prompt. See the package doc.
//
// The vote map is read from the LATEST view per match+voter, because the tally accumulates
// through the phase and an early snapshot would undercount.
func (r *DeceptionRepo) SeatScores(ctx context.Context, limit int) ([]deception.SeatScore, error) {
	const q = `
	WITH m AS (
	  SELECT public_id, state::jsonb->'roles' AS roles
	    FROM matches
	   WHERE game = 'mafia' AND status = 'finished' AND state::jsonb ? 'roles'
	   ORDER BY finished_at DESC NULLS LAST
	   LIMIT $1),
	latest AS (
	  -- One row per (match, DAY, phase), not per match.
	  --
	  -- The tally accumulates within a voting phase and RESETS each day, so the last snapshot of
	  -- a match captures a single day's standing votes and discards every earlier day. Taking the
	  -- latest per match scored one seat out of a whole season; per phase keeps each day's final
	  -- tally exactly once, which is what "who did this seat vote for" actually means.
	  SELECT DISTINCT ON (d.match_id, d.input_json::jsonb->>'day', d.input_json::jsonb->>'phase')
	         d.match_id, d.input_json::jsonb->'votes' AS votes
	    FROM agent_match_decisions d
	    JOIN m ON m.public_id = d.match_id
	   WHERE d.input_json::jsonb ? 'votes'
	   ORDER BY d.match_id, d.input_json::jsonb->>'day', d.input_json::jsonb->>'phase', d.seq DESC),
	v AS (
	  SELECT l.match_id,
	         (jsonb_each_text(l.votes)).key   AS voter,
	         (jsonb_each_text(l.votes)).value AS target
	    FROM latest l)
	SELECT v.voter::int,
	       coalesce(m.roles->>v.voter, ''),
	       count(*)                                                        AS votes_cast,
	       count(*) FILTER (WHERE m.roles->>v.target = 'Mafia')            AS on_mafia,
	       count(*) FILTER (WHERE m.roles->>v.target <> 'Mafia'
	                          AND m.roles->>v.target IS NOT NULL)          AS on_town,
	       count(*) FILTER (WHERE m.roles->>v.voter = 'Mafia'
	                          AND m.roles->>v.target = 'Mafia')            AS on_own_team
	  FROM v JOIN m ON m.public_id = v.match_id
	 WHERE m.roles->>v.voter IS NOT NULL
	 GROUP BY v.voter, m.roles->>v.voter
	 ORDER BY v.voter::int`

	rows, err := r.db.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []deception.SeatScore
	for rows.Next() {
		var s deception.SeatScore
		if err := rows.Scan(&s.Seat, &s.Role, &s.VotesCast, &s.VotesOnMafia,
			&s.VotesOnTown, &s.VotesOnOwnTeam); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
