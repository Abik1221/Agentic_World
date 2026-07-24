package store

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/sdkstats"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SDKStatsRepo is the pgx implementation of sdkstats.Store.
type SDKStatsRepo struct{ db *pgxpool.Pool }

func NewSDKStatsRepo(db *pgxpool.Pool) *SDKStatsRepo { return &SDKStatsRepo{db: db} }

var _ sdkstats.Store = (*SDKStatsRepo)(nil)

func (r *SDKStatsRepo) RecordInstall(ctx context.Context, sdk, version, country string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO sdk_install_pings (sdk, version, country) VALUES ($1, $2, $3)`,
		sdk, version, country)
	return err
}

func (r *SDKStatsRepo) UpsertRegistryDay(ctx context.Context, source string, day time.Time, downloads int64) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO sdk_registry_downloads (source, day, downloads, updated_at)
		 VALUES ($1, $2, $3, now())
		 ON CONFLICT (source, day) DO UPDATE SET downloads = EXCLUDED.downloads, updated_at = now()`,
		source, day, downloads)
	return err
}

// Timeseries buckets install pings (by sdk) and registry downloads (by source) into
// the same period grid (day/week/month) and full-outer-joins them, so the chart has
// one row per period even when only one source has data for it.
func (r *SDKStatsRepo) Timeseries(ctx context.Context, granularity string, since time.Time) ([]sdkstats.TimeseriesRow, error) {
	rows, err := r.db.Query(ctx,
		`WITH pings AS (
		   SELECT date_trunc($1, created_at)::date AS period,
		          COUNT(*) FILTER (WHERE sdk = 'python') AS py,
		          COUNT(*) FILTER (WHERE sdk = 'js')     AS js
		     FROM sdk_install_pings WHERE created_at >= $2 GROUP BY 1
		 ),
		 reg AS (
		   SELECT date_trunc($1, day::timestamp)::date AS period,
		          COALESCE(SUM(downloads) FILTER (WHERE source = 'npm'), 0)  AS npm,
		          COALESCE(SUM(downloads) FILTER (WHERE source = 'pypi'), 0) AS pypi
		     FROM sdk_registry_downloads WHERE day >= $2::date GROUP BY 1
		 )
		 SELECT to_char(COALESCE(p.period, r.period), 'YYYY-MM-DD') AS period,
		        COALESCE(p.py, 0), COALESCE(p.js, 0),
		        COALESCE(r.npm, 0), COALESCE(r.pypi, 0)
		   FROM pings p FULL OUTER JOIN reg r ON p.period = r.period
		  ORDER BY period`,
		granularity, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sdkstats.TimeseriesRow
	for rows.Next() {
		var t sdkstats.TimeseriesRow
		if err := rows.Scan(&t.Period, &t.PythonPings, &t.JSPings, &t.NPMDownloads, &t.PyPIDownloads); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *SDKStatsRepo) Countries(ctx context.Context, limit, offset int) ([]sdkstats.CountryCount, int, error) {
	var total int
	if err := r.db.QueryRow(ctx, `SELECT COUNT(DISTINCT country) FROM sdk_install_pings`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.db.Query(ctx,
		`SELECT country, COUNT(*) AS c FROM sdk_install_pings
		  GROUP BY country ORDER BY c DESC, country LIMIT $1 OFFSET $2`,
		limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []sdkstats.CountryCount
	for rows.Next() {
		var cc sdkstats.CountryCount
		if err := rows.Scan(&cc.Country, &cc.Count); err != nil {
			return nil, 0, err
		}
		out = append(out, cc)
	}
	return out, total, rows.Err()
}

func (r *SDKStatsRepo) Summary(ctx context.Context) (sdkstats.Summary, error) {
	var s sdkstats.Summary
	if err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FILTER (WHERE sdk='python'), COUNT(*) FILTER (WHERE sdk='js'),
		        COUNT(DISTINCT country)
		   FROM sdk_install_pings`).Scan(&s.PythonPings, &s.JSPings, &s.Countries); err != nil {
		return s, err
	}
	if err := r.db.QueryRow(ctx,
		`SELECT COALESCE(SUM(downloads) FILTER (WHERE source='npm'), 0),
		        COALESCE(SUM(downloads) FILTER (WHERE source='pypi'), 0)
		   FROM sdk_registry_downloads`).Scan(&s.NPMDownloads, &s.PyPIDownloads); err != nil {
		return s, err
	}
	return s, nil
}
