package store

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/growthstats"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GrowthStatsRepo is the pgx implementation of growthstats.Store.
type GrowthStatsRepo struct{ db *pgxpool.Pool }

func NewGrowthStatsRepo(db *pgxpool.Pool) *GrowthStatsRepo { return &GrowthStatsRepo{db: db} }

var _ growthstats.Store = (*GrowthStatsRepo)(nil)

func (r *GrowthStatsRepo) SignupCounts(ctx context.Context, since, until time.Time) (int64, error) {
	var n int64
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM users WHERE created_at >= $1 AND created_at < $2`,
		since, until).Scan(&n)
	return n, err
}

func (r *GrowthStatsRepo) TotalUsers(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// Funnel counts users who signed up in [since, until) and later reached each stage
// (lifetime conversion of that cohort — investor-standard).
func (r *GrowthStatsRepo) Funnel(ctx context.Context, since, until time.Time) (growthstats.FunnelCounts, error) {
	var f growthstats.FunnelCounts
	err := r.db.QueryRow(ctx, `
		WITH cohort AS (
		  SELECT id FROM users WHERE created_at >= $1 AND created_at < $2
		)
		SELECT
		  (SELECT COUNT(*) FROM cohort),
		  (SELECT COUNT(DISTINCT c.id) FROM cohort c
		     WHERE EXISTS (
		       SELECT 1 FROM agents a
		        WHERE a.owner_user_id = c.id AND a.kind <> 'house')),
		  (SELECT COUNT(DISTINCT c.id) FROM cohort c
		     WHERE EXISTS (
		       SELECT 1 FROM agents a
		         JOIN agent_keys k ON k.agent_id = a.id
		        WHERE a.owner_user_id = c.id AND a.kind <> 'house'
		          AND k.last_used_at IS NOT NULL AND k.revoked_at IS NULL)
		        OR EXISTS (
		       SELECT 1 FROM match_players mp
		        WHERE mp.owner_user_id = c.id)),
		  (SELECT COUNT(DISTINCT c.id) FROM cohort c
		     WHERE EXISTS (
		       SELECT 1 FROM wallets w
		         JOIN ledger_entries e ON e.wallet_id = w.id
		         JOIN ledger_transactions t ON t.id = e.txn_id
		        WHERE w.user_id = c.id AND t.kind = 'topup' AND e.amount > 0))
	`, since, until).Scan(&f.Signups, &f.BuiltAgent, &f.RanSDK, &f.Paid)
	return f, err
}

// AuthBreakdown assigns each cohort user to one primary provider (priority order
// when multiple identities are linked — rare at signup).
func (r *GrowthStatsRepo) AuthBreakdown(ctx context.Context, since, until time.Time) ([]growthstats.AuthCount, error) {
	rows, err := r.db.Query(ctx, `
		WITH cohort AS (
		  SELECT id, google_sub, github_id, apple_sub, password_hash, privy_user_id
		    FROM users WHERE created_at >= $1 AND created_at < $2
		),
		labeled AS (
		  SELECT CASE
		           WHEN google_sub IS NOT NULL AND google_sub <> '' THEN 'google'
		           WHEN apple_sub  IS NOT NULL AND apple_sub  <> '' THEN 'apple'
		           WHEN github_id  IS NOT NULL AND github_id  <> '' THEN 'github'
		           WHEN password_hash IS NOT NULL THEN 'email'
		           WHEN privy_user_id IS NOT NULL AND privy_user_id <> '' THEN 'privy'
		           ELSE 'other'
		         END AS provider
		    FROM cohort
		)
		SELECT provider, COUNT(*)::bigint
		  FROM labeled
		 GROUP BY provider
		 ORDER BY COUNT(*) DESC, provider
	`, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []growthstats.AuthCount
	var total int64
	for rows.Next() {
		var a growthstats.AuthCount
		if err := rows.Scan(&a.Provider, &a.Count); err != nil {
			return nil, err
		}
		total += a.Count
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if total > 0 {
			out[i].Pct = float64(out[i].Count) / float64(total) * 100
		}
	}
	return out, nil
}

func (r *GrowthStatsRepo) Timeseries(ctx context.Context, since, until time.Time) ([]growthstats.DayRow, error) {
	rows, err := r.db.Query(ctx, `
		WITH days AS (
		  SELECT generate_series(($1::timestamptz AT TIME ZONE 'UTC')::date,
		                         (($2::timestamptz - interval '1 second') AT TIME ZONE 'UTC')::date,
		                         '1 day'::interval)::date AS day
		),
		cohort AS (
		  SELECT id, (created_at AT TIME ZONE 'UTC')::date AS day
		    FROM users WHERE created_at >= $1 AND created_at < $2
		),
		built AS (
		  SELECT DISTINCT c.id, c.day FROM cohort c
		   WHERE EXISTS (SELECT 1 FROM agents a WHERE a.owner_user_id = c.id AND a.kind <> 'house')
		),
		ran AS (
		  SELECT DISTINCT c.id, c.day FROM cohort c
		   WHERE EXISTS (
		           SELECT 1 FROM agents a JOIN agent_keys k ON k.agent_id = a.id
		            WHERE a.owner_user_id = c.id AND a.kind <> 'house'
		              AND k.last_used_at IS NOT NULL AND k.revoked_at IS NULL)
		      OR EXISTS (SELECT 1 FROM match_players mp WHERE mp.owner_user_id = c.id)
		),
		paid AS (
		  SELECT DISTINCT c.id, c.day FROM cohort c
		   WHERE EXISTS (
		     SELECT 1 FROM wallets w
		       JOIN ledger_entries e ON e.wallet_id = w.id
		       JOIN ledger_transactions t ON t.id = e.txn_id
		      WHERE w.user_id = c.id AND t.kind = 'topup' AND e.amount > 0)
		)
		SELECT to_char(d.day, 'YYYY-MM-DD'),
		       COALESCE((SELECT COUNT(*) FROM cohort c WHERE c.day = d.day), 0),
		       COALESCE((SELECT COUNT(*) FROM built b WHERE b.day = d.day), 0),
		       COALESCE((SELECT COUNT(*) FROM ran x WHERE x.day = d.day), 0),
		       COALESCE((SELECT COUNT(*) FROM paid p WHERE p.day = d.day), 0)
		  FROM days d
		 ORDER BY d.day
	`, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []growthstats.DayRow
	for rows.Next() {
		var row growthstats.DayRow
		if err := rows.Scan(&row.Date, &row.Signups, &row.BuiltAgent, &row.RanSDK, &row.Paid); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *GrowthStatsRepo) Countries(ctx context.Context, since, until time.Time, limit, offset int) ([]growthstats.CountryCount, int, error) {
	var total int
	err := r.db.QueryRow(ctx, `
		WITH cohort AS (
		  SELECT CASE
		           WHEN signup_country IS NOT NULL AND signup_country <> '' AND signup_country <> 'XX'
		             THEN upper(signup_country)
		           WHEN country IS NOT NULL AND length(btrim(country)) = 2
		             THEN upper(btrim(country))
		           WHEN country IS NOT NULL AND btrim(country) <> ''
		             THEN btrim(country)
		           ELSE 'XX'
		         END AS country
		    FROM users WHERE created_at >= $1 AND created_at < $2
		)
		SELECT COUNT(DISTINCT country) FROM cohort
	`, since, until).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := r.db.Query(ctx, `
		WITH cohort AS (
		  SELECT
		    CASE
		      WHEN signup_country IS NOT NULL AND signup_country <> '' AND signup_country <> 'XX'
		        THEN upper(signup_country)
		      WHEN country IS NOT NULL AND length(btrim(country)) = 2
		        THEN upper(btrim(country))
		      WHEN country IS NOT NULL AND btrim(country) <> ''
		        THEN btrim(country)
		      ELSE 'XX'
		    END AS country,
		    CASE
		      WHEN signup_country IS NOT NULL AND signup_country <> '' AND signup_country <> 'XX'
		        THEN 'signup_geo'
		      WHEN country IS NOT NULL AND btrim(country) <> ''
		        THEN 'profile'
		      ELSE 'unknown'
		    END AS source
		    FROM users WHERE created_at >= $1 AND created_at < $2
		),
		agg AS (
		  SELECT country,
		         COUNT(*)::bigint AS c,
		         CASE
		           WHEN COUNT(DISTINCT source) > 1 THEN 'mixed'
		           ELSE MIN(source)
		         END AS source
		    FROM cohort
		   GROUP BY country
		)
		SELECT country, c, source FROM agg
		 ORDER BY c DESC, country
		 LIMIT $3 OFFSET $4
	`, since, until, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []growthstats.CountryCount
	for rows.Next() {
		var row growthstats.CountryCount
		if err := rows.Scan(&row.Country, &row.Count, &row.Source); err != nil {
			return nil, 0, err
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
}

// SetSignupCountry records the GeoIP country for a newly created account.
// Idempotent for existing non-XX values so a later login cannot overwrite the first signal.
func (r *IdentityRepo) SetSignupCountry(ctx context.Context, userPublicID, country string) error {
	if country == "" {
		country = "XX"
	}
	_, err := r.db.Exec(ctx,
		`UPDATE users SET signup_country = $2
		  WHERE public_id = $1
		    AND (signup_country IS NULL OR signup_country = '' OR signup_country = 'XX')`,
		userPublicID, country)
	return err
}
