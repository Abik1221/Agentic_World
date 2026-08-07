package store

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/modelboard"
)

// RecordBoardHistory upserts one day's fitted board.
//
// Called on every refresh, upserting on (day, model), so the row holds that day's LATEST fit —
// which is also its most complete one, having seen the most matches. Storing every refresh instead
// would leave ~144 rows per model per day describing the same day, and any chart would then have to
// invent a rule for which one the day "was".
//
// A model absent from today's board is simply not written. It is NOT zeroed: a model that stopped
// playing has no rating today, and writing a zero would draw a line crashing to the bottom of the
// chart for a model that merely went quiet.
func (r *ModelBoardRepo) RecordBoardHistory(ctx context.Context, day time.Time, windowDays int, ratings []modelboard.Rating) error {
	if len(ratings) == 0 {
		return nil
	}
	d := day.UTC().Truncate(24 * time.Hour)
	batch := make([][]any, 0, len(ratings))
	for _, rt := range ratings {
		batch = append(batch, []any{
			d, rt.Model, rt.Elo, rt.EloLow, rt.EloHigh, rt.Theta,
			rt.Rank, rt.RankStability, rt.Comparisons, rt.Wins, rt.Losses, rt.Draws,
			rt.Harnesses, rt.Separability, rt.Provisional, windowDays,
		})
	}
	for _, row := range batch {
		if _, err := r.db.Exec(ctx,
			`INSERT INTO model_board_history (
			     day, model, elo, elo_low, elo_high, theta, rank, rank_stability,
			     comparisons, wins, losses, draws, harnesses, separability, provisional,
			     window_days, computed_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16, now())
			 ON CONFLICT (day, model) DO UPDATE SET
			   elo = EXCLUDED.elo, elo_low = EXCLUDED.elo_low, elo_high = EXCLUDED.elo_high,
			   theta = EXCLUDED.theta, rank = EXCLUDED.rank,
			   rank_stability = EXCLUDED.rank_stability,
			   comparisons = EXCLUDED.comparisons, wins = EXCLUDED.wins,
			   losses = EXCLUDED.losses, draws = EXCLUDED.draws,
			   harnesses = EXCLUDED.harnesses, separability = EXCLUDED.separability,
			   provisional = EXCLUDED.provisional, window_days = EXCLUDED.window_days,
			   computed_at = now()`, row...); err != nil {
			return err
		}
	}
	return nil
}

// BoardHistory returns one model's series, oldest first.
//
// Oldest first because that is the order a chart draws in; returning newest-first would make every
// caller reverse it, and one of them eventually would not.
func (r *ModelBoardRepo) BoardHistory(ctx context.Context, model string, since time.Time) ([]modelboard.HistoryPoint, error) {
	rows, err := r.db.Query(ctx,
		`SELECT day, elo, elo_low, elo_high, rank, rank_stability, comparisons,
		        wins, losses, draws, separability, provisional
		   FROM model_board_history
		  WHERE model = $1 AND day >= $2
		  ORDER BY day ASC`, model, since.UTC().Truncate(24*time.Hour))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []modelboard.HistoryPoint
	for rows.Next() {
		var p modelboard.HistoryPoint
		var day time.Time
		if err := rows.Scan(&day, &p.Elo, &p.EloLow, &p.EloHigh, &p.Rank, &p.RankStability,
			&p.Comparisons, &p.Wins, &p.Losses, &p.Draws, &p.Separability, &p.Provisional); err != nil {
			return nil, err
		}
		p.Day = day.Format("2006-01-02")
		out = append(out, p)
	}
	return out, rows.Err()
}
