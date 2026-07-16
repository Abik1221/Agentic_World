// Command pindex-recompute rebuilds every developer's P-Index from stored match
// history + the active scoring config, and PROVES the engine is deterministic: when
// a developer's current inputs hash matches the hash recorded at their last
// recompute (i.e. nothing changed underneath), the recomputed P-Index MUST equal the
// stored one — any divergence is a determinism bug and exits non-zero.
//
// It doubles as a BACKFILL tool: with -write it persists the recomputed snapshot
// (and re-ranks the season), so a config change or a fresh deploy can rebuild the
// whole board offline.
//
//	go run ./cmd/pindex-recompute            # verify determinism for the current season
//	go run ./cmd/pindex-recompute -write     # recompute + persist (backfill)
//	go run ./cmd/pindex-recompute -season 12 # a specific season
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/agent-arena/arena/internal/config"
	"github.com/agent-arena/arena/internal/pindex"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/rating"
	"github.com/agent-arena/arena/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pindex-recompute:", err)
		os.Exit(1)
	}
}

func run() error {
	seasonFlag := flag.Int("season", -1, "season to recompute (default: current)")
	write := flag.Bool("write", false, "persist recomputed snapshots (backfill) instead of verify-only")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	clock := platform.NewClock()
	ratingSvc := rating.New(store.NewRatingRepo(st.DB), clock, rating.Config{SeasonLength: cfg.SeasonLength}, prometheus.NewRegistry())
	season := *seasonFlag
	if season < 0 {
		season = ratingSvc.CurrentSeason()
	}

	repo := store.NewPIndexRepo(st.DB)
	engine := pindex.NewEngine()
	scoreCfg, err := repo.ActiveConfig(ctx)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	devs, err := developers(ctx, st, season)
	if err != nil {
		return err
	}
	fmt.Printf("recompute: season=%d config=v%d developers=%d write=%v\n", season, scoreCfg.Version, len(devs), *write)

	var checked, reproduced, changed, mismatches int
	svc := pindex.New(repo, ratingSvc.CurrentSeason, clock, nil)
	for _, dev := range devs {
		stored, ok, err := repo.Get(ctx, dev, season)
		if err != nil {
			return err
		}
		asOf := clock.Now()
		if ok {
			asOf = stored.ComputedAt // reproduce AS OF the stored snapshot
		}
		in, err := repo.Inputs(ctx, dev, season, asOf)
		if err != nil {
			return err
		}
		in.UserPublicID, in.Season, in.AsOf = dev, season, asOf
		res := engine.Compute(in, scoreCfg)

		checked++
		storedHash, haveHash, err := lastInputsHash(ctx, st, dev)
		if err != nil {
			return err
		}
		switch {
		case ok && haveHash && storedHash == in.Hash():
			// Inputs unchanged since the last recompute ⇒ value MUST match.
			if res.PIndex != stored.PIndex {
				mismatches++
				fmt.Printf("  DETERMINISM MISMATCH %s: stored=%.2f recomputed=%.2f (same inputs hash)\n", dev, stored.PIndex, res.PIndex)
			} else {
				reproduced++
			}
		default:
			changed++ // inputs moved (new matches / first compute) — recompute is expected to differ
		}

		if *write {
			if err := svc.Recompute(ctx, dev, season); err != nil {
				return fmt.Errorf("write %s: %w", dev, err)
			}
		}
	}
	if *write {
		if err := repo.Rank(ctx, season); err != nil {
			return err
		}
	}

	fmt.Printf("done: checked=%d reproduced=%d changed=%d mismatches=%d\n", checked, reproduced, changed, mismatches)
	if mismatches > 0 {
		return fmt.Errorf("%d determinism mismatch(es) — the engine is NOT reproducing stored values from identical inputs", mismatches)
	}
	return nil
}

// developers lists developer public ids with a P-Index row this season, falling back
// to everyone who has rated activity this season (so a first run can backfill).
func developers(ctx context.Context, st *store.Store, season int) ([]string, error) {
	rows, err := st.DB.Query(ctx,
		`SELECT u.public_id FROM developer_pindex d JOIN users u ON u.id = d.user_id WHERE d.season = $1
		 UNION
		 SELECT DISTINCT u.public_id FROM users u
		   JOIN agents a  ON a.owner_user_id = u.id AND a.kind <> 'house'
		   JOIN ratings r ON r.agent_id = a.id AND r.season = $1`, season)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func lastInputsHash(ctx context.Context, st *store.Store, userPublicID string) (string, bool, error) {
	var hash string
	err := st.DB.QueryRow(ctx,
		`SELECT inputs_hash FROM developer_pindex_history
		 WHERE user_id = (SELECT id FROM users WHERE public_id = $1)
		 ORDER BY computed_at DESC LIMIT 1`, userPublicID).Scan(&hash)
	if err != nil {
		return "", false, nil // no history yet
	}
	return hash, true, nil
}
