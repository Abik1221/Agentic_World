package store

import (
	"context"
	"encoding/json"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/spectator"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SpectatorRepo backs the live arena feed and ticker. Read-only; the round/score
// of each live match is read from the persisted engine-state snapshot.
type SpectatorRepo struct{ db *pgxpool.Pool }

func NewSpectatorRepo(db *pgxpool.Pool) *SpectatorRepo { return &SpectatorRepo{db: db} }

var _ spectator.Repo = (*SpectatorRepo)(nil)

func (r *SpectatorRepo) LiveMatches(ctx context.Context) ([]spectator.LiveMatch, error) {
	rows, err := r.db.Query(ctx,
		`SELECT m.public_id, m.bid, m.total_rounds, COALESCE(m.state, '{}'::jsonb),
		        COALESCE(array_agg(ag.public_id ORDER BY mp.seat), '{}')
		 FROM matches m
		 JOIN match_players mp ON mp.match_id = m.id
		 JOIN agents ag ON ag.id = mp.agent_id
		 WHERE m.status = 'active'
		 GROUP BY m.id
		 ORDER BY m.started_at DESC NULLS LAST
		 LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []spectator.LiveMatch
	for rows.Next() {
		var lm spectator.LiveMatch
		var stateBytes []byte
		if err := rows.Scan(&lm.MatchID, &lm.Bid, &lm.TotalRounds, &stateBytes, &lm.Agents); err != nil {
			return nil, err
		}
		var st gs.State
		if len(stateBytes) > 0 {
			_ = json.Unmarshal(stateBytes, &st)
		}
		lm.Round = st.Round
		lm.Scores = st.Scores
		out = append(out, lm)
	}
	return out, rows.Err()
}

func (r *SpectatorRepo) LiveStats(ctx context.Context) (spectator.LiveStats, error) {
	var s spectator.LiveStats
	err := r.db.QueryRow(ctx,
		`SELECT
		   (SELECT COUNT(*) FROM matches
		      WHERE created_at >= date_trunc('day', now())),
		   (SELECT COALESCE(SUM(bid * 2), 0) FROM matches
		      WHERE status IN ('active','finished')
		        AND COALESCE(started_at, created_at) >= date_trunc('day', now())),
		   (SELECT COALESCE(MAX(mp.coins_delta), 0) FROM match_players mp
		      JOIN matches m ON m.id = mp.match_id
		      WHERE m.finished_at >= date_trunc('day', now()) AND mp.coins_delta > 0),
		   (SELECT COUNT(*) FROM agents WHERE status = 'active')`).
		Scan(&s.MatchesToday, &s.CoinsWageredToday, &s.BiggestWinToday, &s.ActiveAgents)
	return s, err
}
