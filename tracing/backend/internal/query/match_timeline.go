package query

import (
	"encoding/json"
	"time"

	"github.com/gofiber/fiber/v2"
)

// MatchTimeline returns a match's decision-and-chat trail as it actually happened,
// in order, from the durable per-decision events.
//
// This exists because /v1/matches/:id/decisions reads `benchmark_recorded`, which the
// arena emits ONCE, at settlement. That is fine for a match that finished and useless
// for one that did not: a match that crashed, stalled, or was abandoned mid-way has no
// benchmark row at all, so the decisions view shows "no decision data" for precisely
// the matches an operator most needs to look at. The per-decision events land as each
// move happens, so this view survives the match not finishing.
//
// The two views are complementary, not redundant — the aggregate carries end-of-match
// totals this one cannot know, and this one carries the trail the aggregate loses.
func (h Handler) MatchTimeline(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	id := c.Params("match_id")

	// Accept the bare match id or the "match_<id>" trace id, matching MatchDecisions
	// so links from either view resolve without prefix juggling.
	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT event_id, event_type, event_time, status, actor_id, session_id,
		       latency_ms, error_message,
		       argMax(payload_json, ingested_at) AS payload_json
		FROM events_raw
		WHERE organization_id = ?
		  AND event_type IN ('agent_decision','agent_said','agent_say_rejected')
		  AND (run_id = ? OR trace_id = ? OR trace_id = concat('match_', ?))
		GROUP BY event_id, event_type, event_time, status, actor_id, session_id,
		         latency_ms, error_message
		ORDER BY event_time ASC
		LIMIT 5000`, orgID, id, id, id)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	type entry struct {
		EventID   string         `json:"event_id"`
		Type      string         `json:"type"`
		At        time.Time      `json:"at"`
		Status    string         `json:"status"`
		AgentID   string         `json:"agent_id"`
		Game      string         `json:"game"`
		LatencyMS int64          `json:"latency_ms"`
		Error     string         `json:"error,omitempty"`
		Detail    map[string]any `json:"detail,omitempty"`
	}

	out := make([]entry, 0, 256)
	seats := map[string]bool{}
	for rows.Next() {
		var e entry
		var payload string
		if err := rows.Scan(&e.EventID, &e.Type, &e.At, &e.Status, &e.AgentID, &e.Game,
			&e.LatencyMS, &e.Error, &payload); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		if payload != "" {
			_ = json.Unmarshal([]byte(payload), &e.Detail)
		}
		if e.AgentID != "" {
			seats[e.AgentID] = true
		}
		out = append(out, e)
	}

	agents := make([]string, 0, len(seats))
	for a := range seats {
		agents = append(agents, a)
	}

	return c.JSON(fiber.Map{
		"match_id": id,
		"agents":   agents,
		"entries":  out,
		"count":    len(out),
	})
}
