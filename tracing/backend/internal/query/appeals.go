package query

import (
	"encoding/json"
	"time"

	"github.com/gofiber/fiber/v2"
)

type appealRow struct {
	DisputeID string         `json:"dispute_id"`
	MatchID   string         `json:"match_id"`
	AgentID   string         `json:"agent_id,omitempty"`
	Kind      string         `json:"kind"`
	Status    string         `json:"status"`
	At        time.Time      `json:"at"`
	Detail    map[string]any `json:"detail,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// AppealList is GET /v1/appeals — disputes ingested from arena dispute.opened
// (projected as span_failed with the outbox payload).
func (h Handler) AppealList(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	limit := parseIntBounded(c.Query("limit"), 50, 1, 200)
	offset := parseInt(c.Query("offset"), 0)
	matchFilter := c.Query("match")

	where := `organization_id = ?
		AND event_type = 'span_failed'
		AND (operation = 'dispute.opened' OR step_name = 'dispute.opened'
		     OR JSONExtractString(payload_json, 'dispute_id') != '')` + monopolySQL
	args := []any{orgID}
	if matchFilter != "" {
		where += ` AND (run_id = ? OR JSONExtractString(payload_json, 'match') = ?)`
		args = append(args, matchFilter, matchFilter)
	}

	countQ := `SELECT count() FROM events_raw WHERE ` + where
	var total uint64
	if err := h.Store.DB.QueryRowContext(c.UserContext(), countQ, args...).Scan(&total); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT event_time, run_id, actor_id, error_message, argMax(payload_json, ingested_at)
		FROM events_raw
		WHERE `+where+`
		GROUP BY event_time, run_id, actor_id, error_message
		ORDER BY event_time DESC
		LIMIT ? OFFSET ?`, append(append([]any{}, args...), limit, offset)...)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	out := make([]appealRow, 0, limit)
	for rows.Next() {
		var a appealRow
		var payload string
		if err := rows.Scan(&a.At, &a.MatchID, &a.AgentID, &a.Error, &payload); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if payload != "" {
			_ = json.Unmarshal([]byte(payload), &a.Detail)
			if d, ok := a.Detail["dispute_id"].(string); ok {
				a.DisputeID = d
			}
			if k, ok := a.Detail["kind"].(string); ok {
				a.Kind = k
			}
			if m, ok := a.Detail["match"].(string); ok && a.MatchID == "" {
				a.MatchID = m
			}
			if ag, ok := a.Detail["agent"].(string); ok && a.AgentID == "" {
				a.AgentID = ag
			}
		}
		if a.DisputeID == "" {
			a.DisputeID = a.MatchID
		}
		if a.Kind == "" {
			a.Kind = "other"
		}
		a.Status = "open"
		if isMonopolyID(a.MatchID) {
			continue
		}
		out = append(out, a)
	}
	return c.JSON(fiber.Map{"appeals": out, "total": total, "limit": limit, "offset": offset})
}
