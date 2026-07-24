package store

import (
	"context"
	"errors"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/profiles"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProfilesRepo is the pgx implementation of profiles.Repo. Headline stats are the
// season ratings aggregate; recent matches join the opponent + their season ELO.
type ProfilesRepo struct{ db *pgxpool.Pool }

func NewProfilesRepo(db *pgxpool.Pool) *ProfilesRepo { return &ProfilesRepo{db: db} }

var _ profiles.Repo = (*ProfilesRepo)(nil)

func (r *ProfilesRepo) AgentInfo(ctx context.Context, slugOrID string) (profiles.Agent, error) {
	var a profiles.Agent
	err := r.db.QueryRow(ctx,
		`SELECT a.public_id, a.name, a.slug, COALESCE(u.x_handle, ''), a.status, a.verification_level
		 FROM agents a JOIN users u ON u.id = a.owner_user_id
		 WHERE a.slug = $1 OR a.public_id = $1`, slugOrID).
		Scan(&a.PublicID, &a.Name, &a.Slug, &a.XHandle, &a.Status, &a.VerificationLevel)
	if errors.Is(err, pgx.ErrNoRows) {
		return profiles.Agent{}, httpx.ErrNotFound
	}
	return a, err
}

// Badges returns the agent's earned achievements, oldest first.
func (r *ProfilesRepo) Badges(ctx context.Context, agentPublicID string) ([]profiles.Badge, error) {
	rows, err := r.db.Query(ctx,
		`SELECT code, awarded_at FROM agent_badges WHERE agent_public_id = $1 ORDER BY awarded_at`,
		agentPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []profiles.Badge
	for rows.Next() {
		var b profiles.Badge
		if err := rows.Scan(&b.Code, &b.AwardedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Economics returns the agent's lifetime per-game games/wins/cost from the
// benchmark projection (all games, all seasons — "since it first started"). Ordered
// by spend so the costliest game is first. The service folds these into totals.
func (r *ProfilesRepo) Economics(ctx context.Context, agentPublicID string) ([]profiles.GameCost, error) {
	rows, err := r.db.Query(ctx,
		`SELECT COALESCE(NULLIF(amb.game, ''), 'unknown') AS game,
		        COUNT(*)                                   AS games,
		        COUNT(*) FILTER (WHERE amb.result = 'win') AS wins,
		        COALESCE(SUM(amb.estimated_cost), 0)       AS total_cost
		   FROM agent_match_benchmark amb
		   JOIN agents a ON a.id = amb.agent_id
		  WHERE a.public_id = $1
		  GROUP BY 1
		  ORDER BY total_cost DESC`,
		agentPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []profiles.GameCost
	for rows.Next() {
		var g profiles.GameCost
		if err := rows.Scan(&g.Game, &g.Games, &g.Wins, &g.TotalCostUSD); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SeasonHistory returns the agent's per-season standings, newest season first.
func (r *ProfilesRepo) SeasonHistory(ctx context.Context, agentPublicID string) ([]profiles.SeasonElo, error) {
	// Aggregate across arenas: an agent now has one rating row PER GAME per season, so
	// the season top-line is the best displayed rating that season and the summed
	// record. (The per-arena breakdown is served separately.)
	rows, err := r.db.Query(ctx,
		`SELECT r.season, MAX(r.elo), COALESCE(SUM(r.wins),0), COALESCE(SUM(r.losses),0), COALESCE(SUM(r.ties),0)
		 FROM ratings r JOIN agents a ON a.id = r.agent_id
		 WHERE a.public_id = $1
		 GROUP BY r.season
		 ORDER BY r.season DESC
		 LIMIT 24`, agentPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []profiles.SeasonElo
	for rows.Next() {
		var s profiles.SeasonElo
		if err := rows.Scan(&s.Season, &s.Elo, &s.Wins, &s.Losses, &s.Ties); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *ProfilesRepo) Stats(ctx context.Context, agentPublicID string, season int) (profiles.Stats, error) {
	// Aggregate across arenas for the season top-line: best displayed rating + summed
	// record + best streaks. COALESCE handles the unrated case (no rating rows yet).
	var st profiles.Stats
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(MAX(r.elo), 1500), COALESCE(SUM(r.wins),0), COALESCE(SUM(r.losses),0),
		        COALESCE(SUM(r.ties),0), COALESCE(SUM(r.coins_earned),0),
		        COALESCE(MAX(r.current_streak),0), COALESCE(MAX(r.best_streak),0)
		 FROM ratings r JOIN agents a ON a.id = r.agent_id
		 WHERE a.public_id = $1 AND r.season = $2`, agentPublicID, season).
		Scan(&st.Elo, &st.Wins, &st.Losses, &st.Ties, &st.CoinsEarned, &st.CurrentStreak, &st.BestStreak)
	if errors.Is(err, pgx.ErrNoRows) {
		return profiles.Stats{Elo: 1500}, nil // unrated this season
	}
	return st, err
}

func (r *ProfilesRepo) RecentMatches(ctx context.Context, agentPublicID string, season, limit int) ([]profiles.RecentMatch, error) {
	rows, err := r.db.Query(ctx,
		`SELECT m.public_id, COALESCE(mp.coins_delta, 0), COALESCE(mp.final_score, 0),
		        COALESCE(opp_mp.final_score, 0), opp.public_id, COALESCE(re.elo, 1500), m.finished_at,
		        COALESCE(mrc.rating_delta, 0)
		 FROM match_players mp
		 JOIN matches m       ON m.id = mp.match_id
		 JOIN match_players opp_mp ON opp_mp.match_id = m.id AND opp_mp.seat <> mp.seat
		 JOIN agents opp      ON opp.id = opp_mp.agent_id
		 JOIN agents me       ON me.id = mp.agent_id
		 LEFT JOIN ratings re ON re.agent_id = opp.id AND re.game = m.game AND re.season = $2
		 LEFT JOIN match_rating_changes mrc ON mrc.match_id = m.id AND mrc.agent_id = me.id
		 WHERE me.public_id = $1 AND m.status = 'finished'
		 ORDER BY m.finished_at DESC NULLS LAST
		 LIMIT $3`, agentPublicID, season, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []profiles.RecentMatch
	for rows.Next() {
		var rm profiles.RecentMatch
		var finished *time.Time
		if err := rows.Scan(&rm.MatchID, &rm.CoinsDelta, &rm.YourScore, &rm.OppScore,
			&rm.Opponent, &rm.OpponentElo, &finished, &rm.RatingDelta); err != nil {
			return nil, err
		}
		if finished != nil {
			rm.FinishedAt = *finished
		}
		out = append(out, rm)
	}
	return out, rows.Err()
}
