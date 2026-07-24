package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/sdkstats"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSDKStatsRepoIntegration exercises the analytics SQL (install pings + registry
// upserts → summary / paginated countries / full-outer-join timeseries) against a
// REAL Postgres. Skipped unless PYYOL_TEST_DATABASE_URL points at a migrated DB.
func TestSDKStatsRepoIntegration(t *testing.T) {
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL to a migrated Postgres to run this")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	// Clean slate (idempotent test).
	_, _ = pool.Exec(ctx, `TRUNCATE sdk_install_pings, sdk_registry_downloads`)

	r := NewSDKStatsRepo(pool)
	for _, in := range [][3]string{{"python", "1.0", "US"}, {"python", "1.0", "DE"}, {"js", "1.0", "US"}} {
		if err := r.RecordInstall(ctx, in[0], in[1], in[2]); err != nil {
			t.Fatalf("RecordInstall: %v", err)
		}
	}
	day := time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)
	if err := r.UpsertRegistryDay(ctx, sdkstats.SourceNPM, day, 7); err != nil {
		t.Fatal(err)
	}
	if err := r.UpsertRegistryDay(ctx, sdkstats.SourcePyPI, day, 5); err != nil {
		t.Fatal(err)
	}
	// Upsert is idempotent + overwrites.
	if err := r.UpsertRegistryDay(ctx, sdkstats.SourceNPM, day, 9); err != nil {
		t.Fatal(err)
	}

	sum, err := r.Summary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sum.PythonPings != 2 || sum.JSPings != 1 || sum.Countries != 2 || sum.NPMDownloads != 9 || sum.PyPIDownloads != 5 {
		t.Fatalf("summary wrong: %+v", sum)
	}

	rows, total, err := r.Countries(ctx, 25, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 2 || rows[0].Country != "US" || rows[0].Count != 2 {
		t.Fatalf("countries wrong: total=%d rows=%+v", total, rows)
	}
	// Pagination: page size 1 returns just the top country.
	page1, _, _ := r.Countries(ctx, 1, 0)
	if len(page1) != 1 || page1[0].Country != "US" {
		t.Fatalf("paginated countries wrong: %+v", page1)
	}

	ts, err := r.Timeseries(ctx, "day", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	// Registry day (2026-07-23) full-outer-joins with today's ping bucket.
	var regRow, pingRow bool
	for _, row := range ts {
		if row.Period == "2026-07-23" && row.NPMDownloads == 9 && row.PyPIDownloads == 5 {
			regRow = true
		}
		if row.PythonPings == 2 && row.JSPings == 1 {
			pingRow = true
		}
	}
	if !regRow || !pingRow {
		t.Fatalf("timeseries missing rows: reg=%v ping=%v rows=%+v", regRow, pingRow, ts)
	}
}
