// backfill-estimated-cost fills estimated_cost on historical events_raw rows where tokens exist
// but cost was never stored, using the same tiered heuristics as pyyol-api estimate-llm-cost.ts
// (see internal/pricing/llm_cost.go). Optionally rebuilds projection tables from events_raw.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/agent-arena/pyyol-lens/backend/internal/backfill"
	"github.com/agent-arena/pyyol-lens/backend/internal/config"
	"github.com/agent-arena/pyyol-lens/backend/internal/pricing"
	"github.com/agent-arena/pyyol-lens/backend/internal/store"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "only print how many rows would be updated")
	limit := flag.Int("limit", 0, "max rows to scan (0 = no limit)")
	batchSize := flag.Int("batch", 200, "rows per ALTER UPDATE (multiIf batch)")
	reproject := flag.Bool("reproject", false, "after updating events_raw, truncate projection tables and rebuild from events_raw (same as BACKFILL_RESET + backfill-projections)")
	flag.Parse()

	if *batchSize < 1 {
		log.Fatal("-batch must be >= 1")
	}

	cfg := config.Load()
	ch, err := store.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	where := `total_tokens > 0 AND reconciled_cost = 0 AND estimated_cost = 0`
	countQ := `SELECT count() FROM events_raw WHERE ` + where
	var total int64
	if err := ch.DB.QueryRowContext(ctx, countQ).Scan(&total); err != nil {
		log.Fatal(err)
	}
	if *limit > 0 && int64(*limit) < total {
		total = int64(*limit)
	}
	log.Printf("events_raw rows matching %s: %d (limit=%d)", where, total, *limit)

	if *dryRun {
		log.Printf("dry-run: would update up to %d rows", total)
		return
	}

	limitClause := ""
	if *limit > 0 {
		limitClause = fmt.Sprintf(" LIMIT %d", *limit)
	}

	q := `SELECT event_id, provider, model, prompt_tokens, completion_tokens
		FROM events_raw WHERE ` + where + ` ORDER BY event_time` + limitClause

	rs, err := ch.DB.QueryContext(ctx, q)
	if err != nil {
		log.Fatal(err)
	}
	defer rs.Close()

	var buf []costRow
	for rs.Next() {
		var r costRow
		if err := rs.Scan(&r.eventID, &r.provider, &r.model, &r.pt, &r.ct); err != nil {
			log.Fatal(err)
		}
		buf = append(buf, r)
	}
	if err := rs.Err(); err != nil {
		log.Fatal(err)
	}

	updated := 0
	for i := 0; i < len(buf); i += *batchSize {
		end := i + *batchSize
		if end > len(buf) {
			end = len(buf)
		}
		chunk := buf[i:end]
		if err := applyBatch(ctx, ch.DB, chunk); err != nil {
			log.Fatalf("batch %d-%d: %v", i, end, err)
		}
		updated += len(chunk)
		log.Printf("applied batch: %d / %d rows", updated, len(buf))
	}

	log.Printf("finished updating estimated_cost on %d events_raw rows (mutations may still be processing — check system.mutations)", updated)

	if *reproject {
		log.Printf("reproject: truncating projection tables and rebuilding from events_raw")
		if err := backfill.ResetProjectionTables(ctx, ch); err != nil {
			log.Fatal(err)
		}
		if err := backfill.InsertProjectionsFromEventsRaw(ctx, ch); err != nil {
			log.Fatal(err)
		}
		log.Printf("reproject completed")
	}
}

type costRow struct {
	eventID  string
	provider string
	model    string
	pt, ct   int64
}

func applyBatch(ctx context.Context, db *sql.DB, rows []costRow) error {
	if len(rows) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(`ALTER TABLE events_raw UPDATE estimated_cost = multiIf(`)
	for i, r := range rows {
		cost := pricing.EstimateLLMCostUSD(r.provider, r.model, r.pt, r.ct)
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("event_id = ")
		b.WriteString(sqlQuote(r.eventID))
		b.WriteString(", ")
		b.WriteString(strconv.FormatFloat(cost, 'f', 9, 64))
	}
	b.WriteString(", estimated_cost) WHERE event_id IN (")
	for i, r := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(sqlQuote(r.eventID))
	}
	b.WriteString(") SETTINGS mutations_sync = 1")

	_, err := db.ExecContext(ctx, b.String())
	return err
}

func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
