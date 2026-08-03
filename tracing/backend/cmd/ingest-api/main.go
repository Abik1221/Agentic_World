package main

import (
	"log"

	"github.com/agent-arena/pyyol-lens/backend/internal/auth"
	"github.com/agent-arena/pyyol-lens/backend/internal/config"
	"github.com/agent-arena/pyyol-lens/backend/internal/ingest"
	"github.com/agent-arena/pyyol-lens/backend/internal/store"
	"github.com/agent-arena/pyyol-lens/backend/internal/stream"
	"github.com/gofiber/fiber/v2"
)

func main() {
	cfg := config.Load()
	s, err := store.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	js, err := stream.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer js.Close()
	h := ingest.Handler{Config: cfg, Store: s, Stream: js}

	app := fiber.New()
	// Rate limit FIRST, before the key check, so an unauthenticated caller cannot guess
	// the key at line speed. Skips /health internally.
	if cfg.RateLimitPerMinute > 0 {
		app.Use(auth.RateLimit(cfg.RateLimitPerMinute, cfg.TrustedProxyCount))
	}
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true, "service": "ingest-api", "environment": cfg.Environment})
	})

	protected := app.Group("/v1", auth.APIKey(cfg.IngestAPIKey))
	protected.Post("/events/batch", h.IngestBatch)
	protected.Post("/ingest/events", h.IngestEvents)

	log.Fatal(app.Listen(":" + cfg.IngestPort))
}
