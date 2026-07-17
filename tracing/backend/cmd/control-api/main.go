package main

import (
	"log"

	"github.com/agent-arena/pyyol-lens/backend/internal/config"
	"github.com/agent-arena/pyyol-lens/backend/internal/control"
	"github.com/agent-arena/pyyol-lens/backend/internal/store"
	"github.com/gofiber/fiber/v2"
)

func main() {
	cfg := config.Load()
	s, err := store.New(cfg)
	if err != nil {
		log.Printf("control-api: clickhouse unavailable, replay routes will 503: %v", err)
	}
	var st *store.Store
	if err == nil {
		st = s
	}
	h := control.Handler{Config: cfg, Store: st}

	app := fiber.New()
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true, "service": "control-api", "environment": cfg.Environment})
	})

	app.Get("/v1/projects", h.Projects)
	app.Get("/v1/api-keys", h.APIKeys)
	app.Get("/v1/prompts", h.Prompts)
	app.Get("/v1/prompts/:id/versions", h.PromptVersions)
	app.Get("/v1/pricing-models", h.PricingModels)
	app.Get("/v1/datasets", h.Datasets)
	app.Get("/v1/evaluations", h.Evaluations)
	app.Get("/v1/budgets", h.Budgets)
	app.Get("/v1/alerts", h.Alerts)
	app.Get("/v1/policies", h.Policies)
	app.Get("/v1/audit-logs", h.AuditLogs)
	app.Post("/v1/replays", h.LaunchReplay)

	log.Fatal(app.Listen(":" + cfg.ControlPort))
}
