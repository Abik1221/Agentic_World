package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/agent-arena/arena/internal/badges"
	"github.com/agent-arena/arena/internal/devprofile"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DevProfileRepo is the pgx implementation of devprofile.Repo: it reads the
// developer reputation surface (aggregated across a developer's agents) and writes
// the @handle + follow graph.
type DevProfileRepo struct{ db *pgxpool.Pool }

func NewDevProfileRepo(db *pgxpool.Pool) *DevProfileRepo { return &DevProfileRepo{db: db} }

var _ devprofile.Repo = (*DevProfileRepo)(nil)

func (r *DevProfileRepo) ResolveHandle(ctx context.Context, handle string) (devprofile.Identity, bool, error) {
	var id devprofile.Identity
	err := r.db.QueryRow(ctx,
		`SELECT public_id, COALESCE(username::text, ''), COALESCE(display_name, ''),
		        COALESCE(avatar_url, ''), COALESCE(country, ''), segment, created_at
		 FROM users
		 WHERE public_id = $1 OR username = $1
		 ORDER BY (public_id = $1) DESC
		 LIMIT 1`, handle).
		Scan(&id.UserPublicID, &id.Username, &id.DisplayName, &id.AvatarURL, &id.Country, &id.Segment, &id.DeveloperSince)
	if errors.Is(err, pgx.ErrNoRows) {
		return devprofile.Identity{}, false, nil
	}
	if err != nil {
		return devprofile.Identity{}, false, err
	}
	return id, true, nil
}

// LifetimeCoinsEarned sums coins_earned across every season for all of the
// developer's non-house agents — the developer's total net match winnings (in
// coins). Mirrors the per-season aggregate in Stats but without a season filter.
func (r *DevProfileRepo) LifetimeCoinsEarned(ctx context.Context, userPublicID string) (int64, error) {
	var coins int64
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(r.coins_earned),0)
		 FROM ratings r JOIN agents a ON a.id = r.agent_id
		 WHERE a.owner_user_id = (SELECT id FROM users WHERE public_id = $1)
		   AND a.kind <> 'house'`,
		userPublicID).Scan(&coins)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return coins, err
}

func (r *DevProfileRepo) Stats(ctx context.Context, userPublicID string, season int) (devprofile.Stats, []devprofile.ArenaStat, error) {
	var st devprofile.Stats
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(r.wins),0), COALESCE(SUM(r.losses),0), COALESCE(SUM(r.ties),0),
		        COALESCE(MAX(r.current_streak),0), COALESCE(MAX(r.best_streak),0)
		 FROM ratings r JOIN agents a ON a.id = r.agent_id
		 WHERE a.owner_user_id = (SELECT id FROM users WHERE public_id = $1)
		   AND r.season = $2 AND a.kind <> 'house'`,
		userPublicID, season).
		Scan(&st.Wins, &st.Losses, &st.Draws, &st.CurrentWinStreak, &st.LongestWinStreak)
	if err != nil {
		return devprofile.Stats{}, nil, err
	}
	st.TotalMatches = st.Wins + st.Losses + st.Draws

	// Per-arena breakdown (best agent's rating + summed record per game), best first.
	rows, err := r.db.Query(ctx,
		`SELECT r.game, MAX(r.elo), COALESCE(SUM(r.wins),0), COALESCE(SUM(r.losses),0), COALESCE(SUM(r.ties),0)
		 FROM ratings r JOIN agents a ON a.id = r.agent_id
		 WHERE a.owner_user_id = (SELECT id FROM users WHERE public_id = $1)
		   AND r.season = $2 AND a.kind <> 'house'
		 GROUP BY r.game
		 ORDER BY MAX(r.elo) DESC`, userPublicID, season)
	if err != nil {
		return devprofile.Stats{}, nil, err
	}
	defer rows.Close()
	var arenas []devprofile.ArenaStat
	best := -1
	for rows.Next() {
		var a devprofile.ArenaStat
		if err := rows.Scan(&a.Game, &a.Rating, &a.Wins, &a.Losses, &a.Ties); err != nil {
			return devprofile.Stats{}, nil, err
		}
		a.Matches = a.Wins + a.Losses + a.Ties
		if a.Matches > best {
			best, st.FavoriteArena = a.Matches, a.Game
		}
		arenas = append(arenas, a)
	}
	return st, arenas, rows.Err()
}

func (r *DevProfileRepo) Agents(ctx context.Context, userPublicID string, season int) ([]devprofile.AgentCard, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id, a.name, a.slug, a.status, COALESCE(MAX(rt.elo), 1500)
		 FROM agents a
		 LEFT JOIN ratings rt ON rt.agent_id = a.id AND rt.season = $2
		 WHERE a.owner_user_id = (SELECT id FROM users WHERE public_id = $1) AND a.kind <> 'house'
		 GROUP BY a.public_id, a.name, a.slug, a.status
		 ORDER BY COALESCE(MAX(rt.elo), 1500) DESC`, userPublicID, season)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []devprofile.AgentCard
	for rows.Next() {
		var c devprofile.AgentCard
		if err := rows.Scan(&c.PublicID, &c.Name, &c.Slug, &c.Status, &c.BestRating); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *DevProfileRepo) RecentMatches(ctx context.Context, userPublicID string, limit, offset int) ([]devprofile.MatchRow, error) {
	rows, err := r.db.Query(ctx,
		`SELECT m.public_id, m.game, ag.public_id,
		        mrc.rating_before, mrc.rating_after, mrc.rating_delta, mrc.rank_in_match, m.finished_at
		 FROM match_rating_changes mrc
		 JOIN agents  ag ON ag.id = mrc.agent_id
		 JOIN matches m  ON m.id = mrc.match_id
		 WHERE ag.owner_user_id = (SELECT id FROM users WHERE public_id = $1) AND ag.kind <> 'house'
		 ORDER BY mrc.created_at DESC
		 LIMIT $2 OFFSET $3`, userPublicID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []devprofile.MatchRow
	for rows.Next() {
		var m devprofile.MatchRow
		if err := rows.Scan(&m.Match, &m.Game, &m.Agent, &m.RatingBefore, &m.RatingAfter,
			&m.RatingDelta, &m.Rank, &m.FinishedAt); err != nil {
			return nil, err
		}
		m.ReplayURL = fmt.Sprintf("/v1/%s/%s/replay", m.Game, m.Match)
		out = append(out, m)
	}
	return out, rows.Err()
}

// TokenEfficiency sums LLM tokens the developer's non-house agents burned across all
// benchmarked matches, plus how many of those matches were wins (agent_match_benchmark
// carries per-match tokens + result). Zero when no benchmark facts exist yet.
func (r *DevProfileRepo) TokenEfficiency(ctx context.Context, userPublicID string) (int64, int, error) {
	var tokens int64
	var wins int
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(b.tokens),0)::bigint,
		        COUNT(*) FILTER (WHERE b.result = 'win')::int
		 FROM agent_match_benchmark b
		 JOIN agents a ON a.id = b.agent_id
		 WHERE a.owner_user_id = (SELECT id FROM users WHERE public_id = $1) AND a.kind <> 'house'`,
		userPublicID).Scan(&tokens, &wins)
	if err != nil {
		return 0, 0, err
	}
	return tokens, wins, nil
}

// TopModels returns the developer's most-used declared LLM models, ranked by how many
// of their (non-house) agents declare each — mirrors the global ModelBenchmark join
// but scoped to one owner.
func (r *DevProfileRepo) TopModels(ctx context.Context, userPublicID string, limit int) ([]devprofile.ModelUsage, error) {
	rows, err := r.db.Query(ctx,
		`WITH mdl AS (
		   SELECT DISTINCT ON (agent_public_id) agent_public_id, model_provider AS provider, model_name AS model
		   FROM agent_manifests
		   WHERE status <> 'rejected'
		     AND COALESCE(model_provider,'') <> '' AND COALESCE(model_name,'') <> ''
		   ORDER BY agent_public_id, created_at DESC
		 )
		 SELECT mdl.provider, mdl.model, COUNT(*)::int AS agents
		 FROM mdl
		 JOIN agents a ON a.public_id = mdl.agent_public_id
		 WHERE a.owner_user_id = (SELECT id FROM users WHERE public_id = $1) AND a.kind <> 'house'
		 GROUP BY mdl.provider, mdl.model
		 ORDER BY agents DESC, mdl.model
		 LIMIT $2`, userPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []devprofile.ModelUsage
	for rows.Next() {
		var m devprofile.ModelUsage
		if err := rows.Scan(&m.Provider, &m.Model, &m.Agents); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *DevProfileRepo) FollowCounts(ctx context.Context, userPublicID string) (int, int, error) {
	var followers, following int
	err := r.db.QueryRow(ctx,
		`SELECT
		   (SELECT COUNT(*) FROM developer_follows WHERE followee_user_id = u.id),
		   (SELECT COUNT(*) FROM developer_follows WHERE follower_user_id = u.id)
		 FROM users u WHERE u.public_id = $1`, userPublicID).Scan(&followers, &following)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	return followers, following, err
}

func (r *DevProfileRepo) Badges(ctx context.Context, userPublicID string) ([]devprofile.Badge, error) {
	rows, err := r.db.Query(ctx,
		`SELECT code, awarded_at FROM developer_badges
		 WHERE user_id = (SELECT id FROM users WHERE public_id = $1)
		 ORDER BY awarded_at`, userPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []devprofile.Badge
	for rows.Next() {
		var b devprofile.Badge
		if err := rows.Scan(&b.Code, &b.AwardedAt); err != nil {
			return nil, err
		}
		b.Label = badges.Labels[b.Code]
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r *DevProfileRepo) Leaderboard(ctx context.Context, season int, segment string, windowDays, limit, offset int) ([]devprofile.LeaderRow, error) {
	rows, err := r.db.Query(ctx,
		`SELECT u.public_id, COALESCE(u.username::text,''), COALESCE(u.display_name,''),
		        COALESCE(u.avatar_url,''), COALESCE(u.country,''), u.segment,
		        d.p_index, d.global_rank
		 FROM developer_pindex d JOIN users u ON u.id = d.user_id
		 WHERE d.season = $1
		   AND ($2 = 'all' OR u.segment = $2)
		   AND ($3 = 0 OR EXISTS (
		         SELECT 1 FROM match_rating_changes mrc JOIN agents a ON a.id = mrc.agent_id
		         WHERE a.owner_user_id = u.id AND mrc.created_at >= now() - make_interval(days => $3)))
		 ORDER BY d.p_index DESC, d.user_id
		 LIMIT $4 OFFSET $5`, season, segment, windowDays, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []devprofile.LeaderRow
	for rows.Next() {
		var lr devprofile.LeaderRow
		if err := rows.Scan(&lr.Developer, &lr.Username, &lr.DisplayName, &lr.AvatarURL,
			&lr.Country, &lr.Segment, &lr.PIndex, &lr.GlobalRank); err != nil {
			return nil, err
		}
		out = append(out, lr)
	}
	return out, rows.Err()
}

// directoryBaseSQL lists every PUBLIC developer with their season record, LEFT
// JOINing developer_pindex so an unranked developer (signed up, no match yet) is
// still returned — the whole point of the directory vs. the leaderboard.
//
// "Public" = an active user who has opted into a public presence: they either claimed
// an @handle or own at least one real (non-house) agent. House/system owners are
// excluded because their only agents are kind='house'.
const directoryBaseSQL = `
WITH pub AS (
    SELECT u.id, u.public_id,
           COALESCE(u.username::text,'') AS username,
           COALESCE(u.display_name,'')   AS display_name,
           COALESCE(u.avatar_url,'')     AS avatar_url,
           COALESCE(u.country,'')        AS country,
           u.segment, u.created_at
    FROM users u
    WHERE u.status = 'active'
      AND (u.username IS NOT NULL
           OR EXISTS (SELECT 1 FROM agents a
                      WHERE a.owner_user_id = u.id AND a.kind <> 'house'))
), rec AS (
    SELECT a.owner_user_id AS uid,
           COUNT(DISTINCT a.id)::int                        AS agents,
           COALESCE(SUM(rt.wins),0)::int                    AS wins,
           COALESCE(SUM(rt.wins + rt.losses + rt.ties),0)::int AS matches
    FROM agents a
    LEFT JOIN ratings rt ON rt.agent_id = a.id AND rt.season = $1
    WHERE a.kind <> 'house'
    GROUP BY a.owner_user_id
)
SELECT pub.public_id, pub.username, pub.display_name, pub.avatar_url, pub.country, pub.segment,
       COALESCE(d.p_index, 0)::float8, COALESCE(d.global_rank, 0)::int,
       (d.user_id IS NOT NULL) AS ranked,
       COALESCE(rec.matches, 0), COALESCE(rec.wins, 0), COALESCE(rec.agents, 0),
       pub.created_at,
       -- The agent whose name matched the query, when that is WHY this row is here.
       -- Without it a search for an agent returns a developer whose handle looks
       -- nothing like what was typed, and the result reads as a bug.
       COALESCE((
           SELECT a.name FROM agents a
           WHERE a.owner_user_id = pub.id AND a.kind <> 'house' AND $2 <> ''
             AND (a.name ILIKE '%' || $2 || '%' ESCAPE '\'
                  OR a.slug      ILIKE '%' || $2 || '%' ESCAPE '\'
                  OR a.public_id ILIKE '%' || $2 || '%' ESCAPE '\')
           ORDER BY a.created_at
           LIMIT 1
       ), '') AS matched_agent
FROM pub
LEFT JOIN developer_pindex d ON d.user_id = pub.id AND d.season = $1
LEFT JOIN rec              ON rec.uid    = pub.id
WHERE $2 = ''
   OR pub.username     ILIKE '%' || $2 || '%' ESCAPE '\'
   OR pub.display_name ILIKE '%' || $2 || '%' ESCAPE '\'
   OR pub.public_id    ILIKE '%' || $2 || '%' ESCAPE '\'
   -- Agents are how most people know each other here: a developer is far more likely
   -- to be recognised by the bot they shipped than by the handle they registered, so
   -- an agent name, slug or id finds its owner.
   OR EXISTS (
        SELECT 1 FROM agents a
        WHERE a.owner_user_id = pub.id AND a.kind <> 'house'
          AND (a.name ILIKE '%' || $2 || '%' ESCAPE '\'
               OR a.slug      ILIKE '%' || $2 || '%' ESCAPE '\'
               OR a.public_id ILIKE '%' || $2 || '%' ESCAPE '\')
   )
`

// likeEscape neutralises LIKE wildcards in user input so a search for "_" or "%"
// doesn't match everything (the queries declare ESCAPE '\').
func likeEscape(s string) string {
	rep := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return rep.Replace(s)
}

func (r *DevProfileRepo) Directory(ctx context.Context, season int, q, sort string, limit, offset int) ([]devprofile.DirectoryRow, error) {
	// "top": developers who have actually played rank first (that is what the landing
	// spotlight wants), then by P-Index, then by volume. "recent": newest first.
	order := `ORDER BY (COALESCE(rec.matches,0) > 0) DESC,
	                   COALESCE(d.p_index,0) DESC,
	                   COALESCE(rec.matches,0) DESC,
	                   pub.created_at DESC, pub.id`
	if sort == "recent" {
		order = `ORDER BY pub.created_at DESC, pub.id`
	}
	rows, err := r.db.Query(ctx, directoryBaseSQL+order+` LIMIT $3 OFFSET $4`,
		season, likeEscape(q), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []devprofile.DirectoryRow
	for rows.Next() {
		var d devprofile.DirectoryRow
		if err := rows.Scan(&d.Developer, &d.Username, &d.DisplayName, &d.AvatarURL,
			&d.Country, &d.Segment, &d.PIndex, &d.GlobalRank, &d.Ranked,
			&d.Matches, &d.Wins, &d.Agents, &d.JoinedAt, &d.MatchedAgent); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *DevProfileRepo) SetUsername(ctx context.Context, userPublicID, username string) error {
	ct, err := r.db.Exec(ctx,
		`UPDATE users SET username = $2, updated_at = now() WHERE public_id = $1`, userPublicID, username)
	if err != nil {
		if isUniqueViolation(err) {
			return httpx.NewError(http.StatusConflict, "username_taken", "that username is already taken")
		}
		return err
	}
	if ct.RowsAffected() == 0 {
		return httpx.NewError(http.StatusNotFound, "not_found", "no such developer")
	}
	return nil
}

func (r *DevProfileRepo) Follow(ctx context.Context, followerUserPublicID, followeeUserPublicID string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO developer_follows (follower_user_id, followee_user_id)
		 SELECT f.id, t.id FROM users f, users t
		 WHERE f.public_id = $1 AND t.public_id = $2
		 ON CONFLICT DO NOTHING`, followerUserPublicID, followeeUserPublicID)
	return err
}

func (r *DevProfileRepo) Unfollow(ctx context.Context, followerUserPublicID, followeeUserPublicID string) error {
	_, err := r.db.Exec(ctx,
		`DELETE FROM developer_follows
		 WHERE follower_user_id = (SELECT id FROM users WHERE public_id = $1)
		   AND followee_user_id = (SELECT id FROM users WHERE public_id = $2)`,
		followerUserPublicID, followeeUserPublicID)
	return err
}
