package store

import (
	"context"
	"encoding/json"

	"github.com/agent-arena/arena/internal/skill"
)

// Mafia scoring storage.
//
// Mafia is the one game whose skill statistic is not defined on a single decision — lift needs
// a sample, and the decision row does not even record the vote target. So the unit of work here
// is a MATCH, assembled from the event log and the dealt roles, and the seat's verdict is then
// written back onto the votes it cast so that every downstream consumer (the season aggregate,
// the fraud filter, the version stamp) keeps working unchanged. Adding a parallel aggregation
// path instead would have meant reimplementing those filters, and they are subtle enough that a
// second copy would drift.

// NextUnscoredMafiaMatches returns finished mafia matches whose votes are behind the scorer.
//
// Bounded by match rather than by decision because a partially-scored match would produce a
// lift computed from a fraction of a seat's votes — a number that looks like a score and is
// not one.
func (r *SkillRepo) NextUnscoredMafiaMatches(ctx context.Context, version, limit int) ([]skill.MafiaMatchInput, error) {
	if limit <= 0 {
		limit = 25
	}
	rows, err := r.db.Query(ctx, `
		SELECT m.public_id
		  FROM matches m
		 WHERE m.game = 'mafia' AND m.status = 'finished'
		   AND EXISTS (SELECT 1 FROM agent_match_decisions d
		                WHERE d.match_id = m.public_id AND d.action = 'vote'
		                  AND (d.skill_scorer_version IS NULL OR d.skill_scorer_version < $1))
		 ORDER BY m.finished_at DESC
		 LIMIT $2`, version, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]skill.MafiaMatchInput, 0, len(ids))
	for _, id := range ids {
		in := skill.MafiaMatchInput{MatchID: id}

		// Ground truth. team='mafia' is what the engine dealt, not anything a player could see.
		sr, err := r.db.Query(ctx, `
			SELECT ms.seat, COALESCE(ms.agent_id,0), (ms.team = 'mafia')
			  FROM mafia_seats ms JOIN matches m ON m.id = ms.match_id
			 WHERE m.public_id = $1`, id)
		if err != nil {
			return nil, err
		}
		for sr.Next() {
			var s skill.MafiaSeatRow
			if err := sr.Scan(&s.Seat, &s.AgentID, &s.IsMafia); err != nil {
				sr.Close()
				return nil, err
			}
			in.Seats = append(in.Seats, s)
		}
		sr.Close()

		er, err := r.db.Query(ctx, `
			SELECT e.seq, e.type, COALESCE(e.payload::jsonb,'{}'::jsonb)
			  FROM match_events e JOIN matches m ON m.id = e.match_id
			 WHERE m.public_id = $1 AND e.type IN ('vote','eliminate')
			 ORDER BY e.seq`, id)
		if err != nil {
			return nil, err
		}
		for er.Next() {
			var ev skill.MafiaEventRow
			var raw []byte
			if err := er.Scan(&ev.Seq, &ev.Type, &raw); err != nil {
				er.Close()
				return nil, err
			}
			var p struct {
				From   *int `json:"from"`
				Target *int `json:"target"`
				Seat   *int `json:"seat"`
				Day    *int `json:"day"`
			}
			_ = json.Unmarshal(raw, &p)
			if p.From != nil {
				ev.From = *p.From
			}
			if p.Target != nil {
				ev.Target = *p.Target
			}
			if p.Seat != nil {
				ev.Seat = *p.Seat
			}
			if p.Day != nil {
				ev.Day = *p.Day
			}
			in.Events = append(in.Events, ev)
		}
		er.Close()
		out = append(out, in)
	}
	return out, nil
}

// SaveMafiaMatchScores writes each seat's verdict onto the votes that seat cast, and stamps
// every remaining vote in the match so it is not read again.
//
// The stamp-everything step is what keeps the backlog finite. A seat that abstained throughout,
// or a match with roles but no votes, produces no verdict; without a stamp those rows would be
// re-read on every pass forever and the worker would never reach the rest of the history. They
// are stamped with a NULL regret, which is the schema's existing encoding for "looked at, not
// scorable" rather than for "scored zero".
func (r *SkillRepo) SaveMafiaMatchScores(ctx context.Context, matchID string, version int,
	scores []skill.MafiaSeatScore) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, s := range scores {
		if s.AgentID == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE agent_match_decisions
			   SET skill_regret = $3, skill_best = $4, skill_scorer_version = $5
			 WHERE match_id = $1 AND agent_id = $2 AND action = 'vote'`,
			matchID, s.AgentID, s.Regret, s.Skill.Why, version); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agent_match_decisions
		   SET skill_scorer_version = $2
		 WHERE match_id = $1 AND action = 'vote'
		   AND (skill_scorer_version IS NULL OR skill_scorer_version < $2)`,
		matchID, version); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
