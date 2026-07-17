package main

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/agent-arena/pyyol-lens/backend/internal/backfill"
	"github.com/agent-arena/pyyol-lens/backend/internal/config"
	"github.com/agent-arena/pyyol-lens/backend/internal/store"
)

func main() {
	cfg := config.Load()
	ch, err := store.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	reset := strings.EqualFold(os.Getenv("BACKFILL_RESET"), "true")
	if reset {
		if err := backfill.ResetProjectionTables(ctx, ch); err != nil {
			log.Fatal(err)
		}
	}

	if err := backfill.InsertProjectionsFromEventsRaw(ctx, ch); err != nil {
		log.Fatal(err)
	}
	log.Printf("backfill completed (reset=%v)", reset)
}
