package main

import (
	"log"

	"github.com/gofiber/fiber/v2"
	"github.com/agent-arena/pyyol-lens/backend/internal/auth"
	"github.com/agent-arena/pyyol-lens/backend/internal/config"
	"github.com/agent-arena/pyyol-lens/backend/internal/ingest"
	"github.com/agent-arena/pyyol-lens/backend/internal/store"
	"github.com/agent-arena/pyyol-lens/backend/internal/stream"
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
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true, "service": "ingest-api", "environment": cfg.Environment})
	})

	protected := app.Group("/v1", auth.APIKey(cfg.IngestAPIKey))
	protected.Post("/events/batch", h.IngestBatch)
	protected.Post("/ingest/events", h.IngestEvents)

	log.Fatal(app.Listen(":" + cfg.IngestPort))
}
