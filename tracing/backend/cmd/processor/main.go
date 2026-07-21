package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/agent-arena/pyyol-lens/backend/internal/config"
	"github.com/agent-arena/pyyol-lens/backend/internal/processor"
	"github.com/agent-arena/pyyol-lens/backend/internal/store"
	"github.com/agent-arena/pyyol-lens/backend/internal/stream"
)

func main() {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	chStore, err := store.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	// Apply any un-applied ClickHouse migrations before consuming (the container
	// entrypoint only runs SQL on a first-init empty volume, so incremental
	// migrations must be applied in-process). Idempotent + version-tracked.
	if err := chStore.Migrate(ctx); err != nil {
		log.Fatal(err)
	}
	js, err := stream.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer js.Close()

	runner := processor.Runner{Config: cfg, Store: chStore, Stream: js}
	if err := runner.Run(ctx); err != nil && err != context.Canceled {
		log.Fatal(err)
	}
}
