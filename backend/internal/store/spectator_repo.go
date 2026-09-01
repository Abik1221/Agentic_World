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
		 WHERE m.status = 'active' AND m.game = 'goofspiel'
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
			// Skip a row with corrupt state rather than list a zero-value phantom.
			if err := json.Unmarshal(stateBytes, &st); err != nil {
				continue
			}
		}
		lm.Round = st.Round
		lm.Scores = st.Scores
		out = append(out, lm)
	}
	return out, rows.Err()
}

// GamesStatus aggregates per-game live matches, agents playing, and agents waiting.
// goofspiel + mafia share the `matches` table (game column); monopoly has its own
// tables. Waiting = open lobbies (waiting matches / monopoly lobbies) + the ranked
// group queue. Robust to empty tables — every game always appears (zeroes included).
func (r *SpectatorRepo) GamesStatus(ctx context.Context) ([]spectator.GameStatus, error) {
	order := []string{"goofspiel", "mafia"}
	st := map[string]*spectator.GameStatus{}
	for _, g := range order {
		st[g] = &spectator.GameStatus{Game: g}
	}

	// live matches + agents playing (goofspiel/mafia, unified matches table)
	rows, err := r.db.Query(ctx,
		`SELECT m.game, COUNT(DISTINCT m.id), COUNT(mp.agent_id)
		 FROM matches m LEFT JOIN match_players mp ON mp.match_id = m.id
		 WHERE m.status = 'active' AND m.game IN ('goofspiel','mafia')
		 GROUP BY m.game`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var g string
		var live, playing int64
		if err := rows.Scan(&g, &live, &playing); err != nil {
			rows.Close()
			return nil, err
		}
		if s := st[g]; s != nil {
			s.Live, s.Playing = live, playing
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// waiting: open lobbies (waiting matches, goofspiel/mafia)
	if err := r.scanGameCount(ctx,
		`SELECT game, COUNT(*) FROM matches WHERE status='waiting' AND game IN ('goofspiel','mafia') GROUP BY game`,
		func(g string, n int64) {
			if s := st[g]; s != nil {
				s.Waiting += n
			}
		}); err != nil {
		return nil, err
	}
	// waiting: ranked group queue (mafia/monopoly)
	if err := r.scanGameCount(ctx,
		`SELECT game, COUNT(*) FROM group_queue WHERE status='waiting' GROUP BY game`,
		func(g string, n int64) {
			if s := st[g]; s != nil {
				s.Waiting += n
			}
		}); err != nil {
		return nil, err
	}

	out := make([]spectator.GameStatus, 0, len(order))
	for _, g := range order {
		out = append(out, *st[g])
	}
	return out, nil
}

func (r *SpectatorRepo) scanGameCount(ctx context.Context, q string, add func(string, int64)) error {
	rows, err := r.db.Query(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var g string
		var n int64
		if err := rows.Scan(&g, &n); err != nil {
			return err
		}
		add(g, n)
	}
	return rows.Err()
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
