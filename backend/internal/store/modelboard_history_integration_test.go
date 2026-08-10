package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/modelboard"
	"github.com/jackc/pgx/v5/pgxpool"
)

// History is the one thing that cannot be backfilled — a fit describes the matches that existed at
// a moment, and that moment does not return. So the write path is verified against a real database
// rather than trusted, and the upsert semantics are pinned: the day's row must hold that day's
// LATEST fit, not accumulate one row per ten-minute refresh.
func TestBoardHistoryRoundTripIntegration(t *testing.T) {
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// t.Cleanup, not defer. A deferred Close runs when the test function RETURNS, which is
	// before every t.Cleanup — so a cleanup that deletes rows through this pool was running
	// against a closed pool and silently doing nothing (the deletes ignore their errors).
	// Registered FIRST so LIFO ordering runs it LAST, after the data cleanups.
	t.Cleanup(pool.Close)
	repo := NewModelBoardRepo(pool)

	const model = "itest/model-history"
	day := time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC)
	cleanup := func() { _, _ = pool.Exec(ctx, `DELETE FROM model_board_history WHERE model = $1`, model) }
	cleanup()
	t.Cleanup(cleanup)

	first := modelboard.Rating{
		Model: model, Elo: 1520, EloLow: 1480, EloHigh: 1560, Theta: 0.115,
		Rank: 2, RankStability: 0.71, Comparisons: 40, Wins: 25, Losses: 12, Draws: 3,
		Harnesses: 2, Separability: 0.5,
	}
	if err := repo.RecordBoardHistory(ctx, day, 90, []modelboard.Rating{first}); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// A later refresh on the SAME day must replace, not append: the worker refits every ten
	// minutes, and one row per refresh would leave ~144 rows describing one day with no rule for
	// which one the day "was".
	later := first
	later.Elo, later.Comparisons, later.Rank = 1533, 61, 1
	if err := repo.RecordBoardHistory(ctx, day.Add(9*time.Hour), 90, []modelboard.Rating{later}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	pts, err := repo.BoardHistory(ctx, model, day.AddDate(0, 0, -1))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(pts) != 1 {
		t.Fatalf("got %d points for one day, want 1 — the upsert is appending per refresh", len(pts))
	}
	p := pts[0]
	if p.Elo != 1533 || p.Comparisons != 61 || p.Rank != 1 {
		t.Errorf("the day kept the EARLIER fit: %+v", p)
	}
	if p.Day != "2026-08-07" {
		t.Errorf("day = %q, want 2026-08-07 — a later-in-day write moved the bucket", p.Day)
	}
	// The interval must survive: a series without it invites reading a small move as a change.
	if p.EloLow != 1480 || p.EloHigh != 1560 {
		t.Errorf("interval lost: [%v,%v]", p.EloLow, p.EloHigh)
	}
	if p.Separability != 0.5 {
		t.Errorf("separability lost: %v — a rating that rose while separability fell is a "+
			"statement about one developer, and the series must be able to show that", p.Separability)
	}

	// A second day must be its own point, ordered oldest-first for the chart.
	next := first
	next.Elo = 1499
	if err := repo.RecordBoardHistory(ctx, day.AddDate(0, 0, 1), 90, []modelboard.Rating{next}); err != nil {
		t.Fatalf("next day: %v", err)
	}
	pts, _ = repo.BoardHistory(ctx, model, day.AddDate(0, 0, -1))
	if len(pts) != 2 {
		t.Fatalf("got %d points across two days, want 2", len(pts))
	}
	if pts[0].Day >= pts[1].Day {
		t.Errorf("series is not oldest-first: %s then %s", pts[0].Day, pts[1].Day)
	}
}

func TestNoRatingsWritesNothingRatherThanZeroes(t *testing.T) {
	// A model absent from today's board must not be written as a zero. A model that stopped playing
	// has no rating today; a zero would draw its line crashing to the bottom of the chart, which is
	// a claim about its strength rather than about its absence.
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, _ := pgxpool.New(ctx, dsn)
	// t.Cleanup, not defer. A deferred Close runs when the test function RETURNS, which is
	// before every t.Cleanup — so a cleanup that deletes rows through this pool was running
	// against a closed pool and silently doing nothing (the deletes ignore their errors).
	// Registered FIRST so LIFO ordering runs it LAST, after the data cleanups.
	t.Cleanup(pool.Close)
	repo := NewModelBoardRepo(pool)
	if err := repo.RecordBoardHistory(ctx, time.Now(), 90, nil); err != nil {
		t.Fatalf("empty write errored: %v", err)
	}
}
