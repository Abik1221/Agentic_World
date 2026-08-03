package main

import (
	"log"

	"github.com/agent-arena/pyyol-lens/backend/internal/auth"
	"github.com/agent-arena/pyyol-lens/backend/internal/config"
	"github.com/agent-arena/pyyol-lens/backend/internal/control"
	"github.com/agent-arena/pyyol-lens/backend/internal/store"
	"github.com/gofiber/fiber/v2"
)

func main() {
	cfg := config.Load()
	// Fail closed in production, matching the query-api. The control plane's routes are
	// placeholders today, but the names say what they will serve — projects, api-keys,
	// policies, audit-logs — and every one of those is something that must never answer
	// an unauthenticated caller. Requiring the key now means filling a stub in cannot
	// quietly publish it later, which is the failure mode of "we'll add auth when there
	// is something to protect".
	if cfg.Environment == "production" && cfg.QueryAPIKey == "" {
		log.Fatal("QUERY_API_KEY must be set in production (it gates the control-api read plane)")
	}
	if cfg.QueryAPIKey == "" {
		log.Println("WARNING: QUERY_API_KEY unset — control-api /v1 is UNAUTHENTICATED (dev only; bind to localhost)")
	}
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
	// Rate limit FIRST, before the key check, so an unauthenticated caller cannot guess
	// the key at line speed. Skips /health internally.
	if cfg.RateLimitPerMinute > 0 {
		app.Use(auth.RateLimit(cfg.RateLimitPerMinute, cfg.TrustedProxyCount))
	}
	// Liveness stays open: the web topbar pings it for the service-status chips, and it
	// reveals nothing beyond "this process is up".
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true, "service": "control-api", "environment": cfg.Environment})
	})

	// Shared-secret gate over the whole read plane, keyed on the same X-Pyyol-Key the
	// web client already sends. Mounted as a group rather than per-route so a future
	// endpoint is protected by being added, not by someone remembering to guard it.
	v1 := app.Group("/v1")
	if cfg.QueryAPIKey != "" {
		v1.Use(auth.APIKey(cfg.QueryAPIKey))
	}

	v1.Get("/projects", h.Projects)
	v1.Get("/api-keys", h.APIKeys)
	v1.Get("/prompts", h.Prompts)
	v1.Get("/prompts/:id/versions", h.PromptVersions)
	v1.Get("/pricing-models", h.PricingModels)
	v1.Get("/datasets", h.Datasets)
	v1.Get("/evaluations", h.Evaluations)
	v1.Get("/budgets", h.Budgets)
	v1.Get("/alerts", h.Alerts)
	v1.Get("/policies", h.Policies)
	v1.Get("/audit-logs", h.AuditLogs)
	// Replay is a WRITE and keeps its own, stricter admin-key check inside the handler:
	// reading the control plane and launching work on it are different privileges.
	v1.Post("/replays", h.LaunchReplay)

	log.Fatal(app.Listen(":" + cfg.ControlPort))
}
