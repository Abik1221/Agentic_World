package main

import (
	"log"

	"github.com/agent-arena/pyyol-lens/backend/internal/config"
	"github.com/agent-arena/pyyol-lens/backend/internal/query"
	"github.com/agent-arena/pyyol-lens/backend/internal/store"
	"github.com/gofiber/fiber/v2"
)

func main() {
	cfg := config.Load()
	s, err := store.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	h := query.Handler{Config: cfg, Store: s}

	app := fiber.New()
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true, "service": "query-api", "environment": cfg.Environment})
	})
	app.Get("/health/ready", h.HealthReady)
	app.Use("/v1", func(c *fiber.Ctx) error {
		// Projections/ops endpoints can be queried without org header for platform diagnostics.
		if len(c.Path()) >= len("/v1/projections") && c.Path()[:len("/v1/projections")] == "/v1/projections" {
			return c.Next()
		}
		if c.Get("x-organization-id") == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "missing x-organization-id header"})
		}
		return c.Next()
	})

	app.Get("/v1/traces", h.Traces)
	app.Get("/v1/traces/:trace_id", h.TraceByID)
	app.Get("/v1/traces/:trace_id/tree", h.TraceTree)
	app.Get("/v1/traces/:trace_id/events", h.TraceEvents)
	app.Get("/v1/usage/summary", h.UsageSummary)
	app.Get("/v1/costs/summary", h.CostsSummary)
	app.Get("/v1/cost-analytics", h.CostAnalytics)
	app.Get("/v1/retrievals", h.Retrievals)
	app.Get("/v1/tool-calls", h.ToolCalls)
	app.Get("/v1/evaluations", h.Evaluations)
	app.Get("/v1/evaluations/:run_id", h.EvaluationByID)
	app.Get("/v1/prompts", h.Prompts)
	app.Get("/v1/prompts/:id/versions", h.PromptVersions)
	app.Get("/v1/replays", h.Replays)
	app.Get("/v1/replays/:id", h.ReplayByID)
	app.Get("/v1/search/traces", h.SearchTraces)
	app.Get("/v1/search/events", h.SearchEvents)
	app.Get("/v1/metrics/overview", h.Overview)
	app.Get("/v1/projections/status", h.ProjectionStatus)
	app.Get("/v1/projections/status/summary", h.ProjectionStatusSummary)
	app.Get("/v1/projections/metrics", h.ProjectionMetrics)
	app.Get("/v1/projections/metrics/prometheus", h.ProjectionMetricsPrometheus)

	// Compatibility routes for the prototype UI and existing integrations.
	app.Get("/v1/runs", h.Runs)
	app.Get("/v1/runs/:run_id", h.RunByID)
	app.Get("/v1/traces/:trace_id/timeline", h.TraceTimeline)
	app.Get("/v1/matches/:match_id", h.MatchByID)
	app.Get("/v1/metrics/tools", h.ToolCalls)

	// Agent benchmarks: reliability + latency leaderboard and per-agent breakdown.
	app.Get("/v1/benchmarks/agents", h.AgentLeaderboard)
	app.Get("/v1/benchmarks/agents/:agent_id", h.AgentBenchmark)
	app.Get("/v1/benchmarks/agents/:agent_id/versions", h.AgentVersions)
	app.Get("/v1/benchmarks/providers", h.ProviderBenchmarks)

	log.Fatal(app.Listen(":" + cfg.QueryPort))
}
