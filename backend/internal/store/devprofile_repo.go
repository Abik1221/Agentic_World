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
	"time"
)

// DevProfileRepo is the pgx implementation of devprofile.Repo: it reads the
// developer reputation surface (aggregated across a developer's agents) and writes
// the @handle + follow graph.
type DevProfileRepo struct{ db *pgxpool.Pool }

func NewDevProfileRepo(db *pgxpool.Pool) *DevProfileRepo { return &DevProfileRepo{db: db} }

var _ devprofile.Repo = (*DevProfileRepo)(nil)

func (r *DevProfileRepo) ResolveHandle(ctx context.Context, handle string) (devprofile.Identity, bool, error) {
	var id devprofile.Identity
	// The bio comes from the same row SetProfile writes it to: the developer's oldest
	// non-house agent. It is a correlated subquery rather than a join so a developer with
	// no agent yet still resolves (LEFT JOIN would work too; this keeps one row per user
	// guaranteed by construction rather than by the ORDER BY).
	err := r.db.QueryRow(ctx,
		`SELECT u.public_id, COALESCE(u.username::text, ''), COALESCE(u.display_name, ''),
		        COALESCE((SELECT a.bio FROM agents a
		                   WHERE a.owner_user_id = u.id AND a.kind <> 'house'
		                   ORDER BY a.id ASC LIMIT 1), ''),
		        COALESCE(u.avatar_url, ''), COALESCE(u.country, ''), u.segment, u.created_at
		 FROM users u
		 WHERE u.public_id = $1 OR u.username = $1
		 ORDER BY (u.public_id = $1) DESC
		 LIMIT 1`, handle).
		Scan(&id.UserPublicID, &id.Username, &id.DisplayName, &id.Bio, &id.AvatarURL, &id.Country, &id.Segment, &id.DeveloperSince)
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

// SandboxActivity counts a developer's unrated practice matches.
//
// Reads from `matches` (mode = 'sandbox'), NOT from ratings — sandbox never writes a
// ratings row, which is exactly why this needs its own query rather than a filter on
// the reputation ones. Those stay untouched and stay competitive-only.
//
// House agents are excluded from the owner side so the developer's own seat is
// counted once, not once per opponent on the table.
func (r *DevProfileRepo) SandboxActivity(ctx context.Context, userPublicID string) (devprofile.SandboxStats, error) {
	out := devprofile.SandboxStats{ByGame: map[string]int{}}
	rows, err := r.db.Query(ctx,
		`SELECT m.game, COUNT(DISTINCT m.id)::int, MAX(m.finished_at)
		   FROM matches m
		   JOIN match_players mp ON mp.match_id = m.id
		   JOIN agents a         ON a.id = mp.agent_id
		  WHERE a.owner_user_id = (SELECT id FROM users WHERE public_id = $1)
		    AND a.kind <> 'house'
		    AND m.mode = 'sandbox'
		    AND m.status = 'finished'
		  GROUP BY m.game`, userPublicID)
	if err != nil {
		return devprofile.SandboxStats{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var game string
		var n int
		var last *time.Time
		if err := rows.Scan(&game, &n, &last); err != nil {
			return devprofile.SandboxStats{}, err
		}
		out.ByGame[game] = n
		out.TotalMatches += n
		if last != nil && (out.LastPlayed == nil || last.After(*out.LastPlayed)) {
			out.LastPlayed = last
		}
	}
	return out, rows.Err()
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

// IsFollowing reports whether follower already follows followee.
func (r *DevProfileRepo) IsFollowing(ctx context.Context, followerUserPublicID, followeeUserPublicID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM developer_follows df
		    WHERE df.follower_user_id = (SELECT id FROM users WHERE public_id = $1)
		      AND df.followee_user_id = (SELECT id FROM users WHERE public_id = $2)
		 )`, followerUserPublicID, followeeUserPublicID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return exists, err
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
//
// PARAMETERS. $1 season, $2 free-text query, $3 "only this developer" and $4 "not this
// developer" — the last two both empty for a plain listing. They exist so the signed-in
// visitor's own row can be pulled out and shown to them separately while being removed
// from the list, WITHOUT the count and the paging going wrong. Filtering self out in the
// browser instead would leave a page of 23 rows claiming to be 24 and a total one too
// high, which is the off-by-one that makes the last page render empty.
//
// Every output column is ALIASED. Not cosmetic: DirectoryCount and DirectoryRowFor wrap
// this query as a subselect, and an unaliased `COALESCE(...)` cannot be referenced from
// the outer WHERE.
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
SELECT pub.public_id AS public_id, pub.username AS username, pub.display_name AS display_name,
       pub.avatar_url AS avatar_url, pub.country AS country, pub.segment AS segment,
       COALESCE(d.p_index, 0)::float8 AS p_index, COALESCE(d.global_rank, 0)::int AS global_rank,
       (d.user_id IS NOT NULL) AS ranked,
       COALESCE(rec.matches, 0) AS matches, COALESCE(rec.wins, 0) AS wins,
       COALESCE(rec.agents, 0) AS agents,
       pub.created_at AS created_at,
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
WHERE (
        $2 = ''
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
   )
   -- The query group above is PARENTHESISED. Without the brackets these two AND terms
   -- would bind to the last OR branch only, so an empty query would stop matching
   -- everyone and the filters would apply to one arm of the search.
   AND ($3 = '' OR pub.public_id = $3)
   AND ($4 = '' OR pub.public_id <> $4)
`

// likeEscape neutralises LIKE wildcards in user input so a search for "_" or "%"
// doesn't match everything (the queries declare ESCAPE '\').
func likeEscape(s string) string {
	rep := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return rep.Replace(s)
}

// DirectoryCount is how many developers match, ignoring the page.
//
// Needed because the directory could only offer "load more": with no total, the UI cannot
// say how many results there are, cannot number pages, and cannot tell a visitor whether
// they are looking at 20 developers or the first 20 of 400. It wraps the same base query so
// the count and the rows can never disagree about what "matching" means — a count computed
// from a second, hand-maintained WHERE clause is a number that goes wrong quietly.
func (r *DevProfileRepo) DirectoryCount(ctx context.Context, season int, q, exclude string) (int, error) {
	var n int
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM (`+directoryBaseSQL+`) matched`,
		season, likeEscape(q), "", exclude).Scan(&n)
	return n, err
}

// DirectoryRowFor returns ONE developer's directory row — the signed-in visitor's own,
// so the page can show it to them separately from the list it has been removed from.
//
// Deliberately the same query as the list rather than a second hand-written one: the row
// shown at the top of the page and the rows in it must agree about what a developer's
// record is, and two queries computing "matches" and "ranked" separately is how they
// come to disagree. Not found is (row, false, nil): a developer who is not PUBLIC yet
// has no directory row, which is a real answer and not an error.
func (r *DevProfileRepo) DirectoryRowFor(ctx context.Context, season int, developerID string) (devprofile.DirectoryRow, bool, error) {
	if developerID == "" {
		return devprofile.DirectoryRow{}, false, nil
	}
	rows, err := r.db.Query(ctx, directoryBaseSQL+` LIMIT 1`, season, "", developerID, "")
	if err != nil {
		return devprofile.DirectoryRow{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return devprofile.DirectoryRow{}, false, rows.Err()
	}
	d, err := scanDirectoryRow(rows)
	if err != nil {
		return devprofile.DirectoryRow{}, false, err
	}
	return d, true, nil
}

func scanDirectoryRow(rows pgx.Rows) (devprofile.DirectoryRow, error) {
	var d devprofile.DirectoryRow
	if err := rows.Scan(&d.Developer, &d.Username, &d.DisplayName, &d.AvatarURL,
		&d.Country, &d.Segment, &d.PIndex, &d.GlobalRank, &d.Ranked,
		&d.Matches, &d.Wins, &d.Agents, &d.JoinedAt, &d.MatchedAgent); err != nil {
		return devprofile.DirectoryRow{}, err
	}
	return d, nil
}

func (r *DevProfileRepo) Directory(ctx context.Context, season int, q, sort string, limit, offset int, exclude string) ([]devprofile.DirectoryRow, error) {
	// "top": developers who have actually played rank first (that is what the landing
	// spotlight wants), then by P-Index, then by volume. "recent": newest first.
	order := `ORDER BY (COALESCE(rec.matches,0) > 0) DESC,
	                   COALESCE(d.p_index,0) DESC,
	                   COALESCE(rec.matches,0) DESC,
	                   pub.created_at DESC, pub.id`
	if sort == "recent" {
		order = `ORDER BY pub.created_at DESC, pub.id`
	}
	rows, err := r.db.Query(ctx, directoryBaseSQL+order+` LIMIT $5 OFFSET $6`,
		season, likeEscape(q), "", exclude, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []devprofile.DirectoryRow
	for rows.Next() {
		d, err := scanDirectoryRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetProfile writes the developer's public identity.
//
// On USERS, not agents. Both tables carry display_name/avatar_url — agents since 0019,
// users since 0031 — and ResolveHandle, which backs every profile response, reads the
// USERS row. Writing the agent row instead stored the value somewhere nothing reads:
// the save would report success and the profile would still come back empty, which is
// indistinguishable from the original bug this was meant to fix.
//
// `bio` lives only on agents, so it is written there in the same transaction, keyed to
// the developer's oldest non-house agent so it stays put when they add more.
//
// nil fields are left alone. COALESCE($n, column) does that in one statement rather than
// building SQL per combination of present fields: a nil pointer marshals to NULL, and
// COALESCE(NULL, display_name) is the value already there.
func (r *DevProfileRepo) SetProfile(ctx context.Context, userPublicID string, displayName, bio, avatarURL *string) error {
	// Nothing to do — and importantly not an error: a caller that computed an empty
	// patch should get a successful no-op, not a failed save to retry.
	if displayName == nil && bio == nil && avatarURL == nil {
		return nil
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if displayName != nil || avatarURL != nil {
		ct, err := tx.Exec(ctx,
			`UPDATE users
			    SET display_name = COALESCE($2, display_name),
			        avatar_url   = COALESCE($3, avatar_url),
			        updated_at   = now()
			  WHERE public_id = $1`,
			userPublicID, displayName, avatarURL)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return httpx.NewError(http.StatusNotFound, "not_found", "no such developer")
		}
	}

	// Best-effort: a developer with no agent yet still gets a name and a photo. Only
	// the bio has nowhere to go, and refusing the whole save for that would block
	// onboarding on a field nobody has filled in yet.
	if bio != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE agents SET bio = $2, updated_at = now()
			 WHERE id = (
			   SELECT a.id FROM agents a
			    WHERE a.owner_user_id = (SELECT id FROM users WHERE public_id = $1)
			      AND a.kind <> 'house'
			    ORDER BY a.id ASC LIMIT 1
			 )`, userPublicID, *bio); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ConnectedWallet returns the developer's connected wallet address, or "" when they
// have none. Backs the "connect your wallet" step of profile completion.
//
// Takes EITHER the hint or the proven address, whichever is present. Two reasons:
//
//   - Completion asks "have you linked a wallet", while a PROVEN wallet is the stricter
//     thing that gates a payout. Requiring proof would leave a developer who connected
//     Phantom stuck below 100% until they had also signed a challenge for money they
//     have not tried to withdraw.
//   - wallet_address alone was not enough. Until the connect-recording endpoint existed
//     it was written ONLY by the Privy login path, so a developer who connected Phantom
//     directly had both columns empty and this step could never tick no matter what they
//     did. Reading both closes that for accounts that verified a wallet before the
//     endpoint shipped.
func (r *DevProfileRepo) ConnectedWallet(ctx context.Context, userPublicID string) (string, error) {
	var addr string
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(NULLIF(wallet_address, ''), NULLIF(verified_wallet_address, ''), '')
		   FROM users WHERE public_id = $1`,
		userPublicID).Scan(&addr)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return addr, err
}

// VerifiedWallet returns ONLY the ownership-proven payout address — the one the developer
// signed a challenge for — or "" when they have not proven one.
//
// Strictly narrower than ConnectedWallet above, and the two are now separate checklist
// steps because they are separate pieces of work with separate consequences: connecting
// shares a public address, while verifying is what makes a payout possible at all. Rolling
// them into one step meant a developer read 100% complete and then discovered at the
// moment of cashing out that there was another thing to do.
func (r *DevProfileRepo) VerifiedWallet(ctx context.Context, userPublicID string) (string, error) {
	var addr string
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(verified_wallet_address, '') FROM users WHERE public_id = $1`,
		userPublicID).Scan(&addr)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return addr, err
}

// SetAvatarURL points the developer at a stored image, touching ONLY avatar_url.
//
// Separate from SetProfile on purpose. SetProfile writes name + bio + avatar as one
// value, so reusing it for an upload would need the caller to send the current name
// and bio back — and an upload that arrives while the developer is mid-edit would then
// overwrite their unsaved text with a stale copy. Uploading a photo should change the
// photo and nothing else.
func (r *DevProfileRepo) SetAvatarURL(ctx context.Context, userPublicID, avatarURL string) error {
	ct, err := r.db.Exec(ctx,
		`UPDATE users SET avatar_url = $2, updated_at = now() WHERE public_id = $1`,
		userPublicID, avatarURL)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return httpx.NewError(http.StatusNotFound, "not_found", "no such developer")
	}
	return nil
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
