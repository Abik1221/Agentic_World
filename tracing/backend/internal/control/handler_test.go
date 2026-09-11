package control

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestUnshippedControlRoutesReturn501NotEmptyJSON(t *testing.T) {
	h := Handler{}
	app := fiber.New()
	app.Get("/projects", h.Projects)
	app.Get("/alerts", h.Alerts)
	app.Get("/budgets", h.Budgets)

	for _, path := range []string{"/projects", "/alerts", "/budgets"} {
		req := httptest.NewRequest("GET", path, nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if resp.StatusCode != fiber.StatusNotImplemented {
			t.Fatalf("%s: got %d, want 501 (empty 200 would look like a live empty dataset)", path, resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("%s: body is not JSON: %s", path, body)
		}
		if payload["error"] != "not_implemented" {
			t.Fatalf("%s: error=%v", path, payload["error"])
		}
	}
}
