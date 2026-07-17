package control

import (
	"context"
	"strings"

	"github.com/agent-arena/pyyol-lens/backend/internal/config"
	"github.com/agent-arena/pyyol-lens/backend/internal/schema"
	"github.com/agent-arena/pyyol-lens/backend/internal/store"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type Handler struct {
	Config config.Config
	Store  *store.Store
}

func (h Handler) Projects(c *fiber.Ctx) error {
	return c.JSON([]fiber.Map{})
}

func (h Handler) APIKeys(c *fiber.Ctx) error {
	return c.JSON([]fiber.Map{})
}

func (h Handler) Prompts(c *fiber.Ctx) error {
	return c.JSON([]fiber.Map{})
}

func (h Handler) PromptVersions(c *fiber.Ctx) error {
	return c.JSON([]fiber.Map{})
}

func (h Handler) PricingModels(c *fiber.Ctx) error {
	return c.JSON([]fiber.Map{})
}

func (h Handler) Datasets(c *fiber.Ctx) error {
	return c.JSON([]fiber.Map{})
}

func (h Handler) Evaluations(c *fiber.Ctx) error {
	return c.JSON([]fiber.Map{})
}

func (h Handler) Budgets(c *fiber.Ctx) error {
	return c.JSON([]fiber.Map{})
}

func (h Handler) Alerts(c *fiber.Ctx) error {
	return c.JSON([]fiber.Map{})
}

func (h Handler) Policies(c *fiber.Ctx) error {
	return c.JSON([]fiber.Map{})
}

func (h Handler) AuditLogs(c *fiber.Ctx) error {
	return c.JSON([]fiber.Map{})
}

func (h Handler) LaunchReplay(c *fiber.Ctx) error {
	if err := requireAdmin(c, h.Config); err != nil {
		return err
	}
	if h.Store == nil {
		return c.Status(503).JSON(fiber.Map{"error": "store not configured"})
	}
	orgID := strings.TrimSpace(c.Get("x-organization-id"))
	if orgID == "" {
		return c.Status(400).JSON(fiber.Map{"error": "missing x-organization-id header"})
	}
	// body optional; for generic replay
	var body map[string]any
	_ = c.BodyParser(&body)
	replayID := uuid.NewString()
	req := store.ReplayRequest{
		ReplayID:       replayID,
		TraceID:        c.Query("trace_id"),
		RunID:          c.Query("run_id"),
		Mode:           "legacy",
		Environment:    h.Config.Environment,
		OrganizationID: orgID,
		RequestJSON:    schema.JSONString(body),
	}
	_ = h.Store.InsertReplay(context.Background(), req)
	return c.JSON(fiber.Map{
		"replay_id": replayID,
		"status":    "queued",
	})
}

func requireOrg(c *fiber.Ctx) string {
	return strings.TrimSpace(c.Get("x-organization-id"))
}

func requireAdmin(c *fiber.Ctx, cfg config.Config) error {
	if cfg.AdminAPIKey == "" {
		return fiber.NewError(fiber.StatusServiceUnavailable, "admin replay API disabled: set PYYOL_LENS_ADMIN_KEY")
	}
	if c.Get("X-Pyyol-Admin-Key") != cfg.AdminAPIKey {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid or missing X-Pyyol-Admin-Key")
	}
	return nil
}
