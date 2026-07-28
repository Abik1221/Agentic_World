package query

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

// AgentActivity is the read side of the developer-facing trace view.
//
// IMPORTANT — where the trust boundary is. This service has no notion of a user: it
// authenticates a shared operator key and trusts x-organization-id verbatim. It
// therefore CANNOT decide whether a caller owns an agent, and it does not try to.
// Ownership is enforced one layer up, in the arena backend, which is the only place
// that knows which user owns which agent.
//
// What this endpoint does instead is refuse to be a wildcard. Both filters —
// which agents, and which event types — are REQUIRED and explicit. There is no
// "omit the filter to get everything" mode, because that is exactly the mode a
// caller-side bug would silently fall into. An empty list yields an empty result,
// never the whole table.
//
// The arena backend passes its own visibility allowlist as the event_type filter, so
// the policy has a single definition (telemetry.DevVisibleEventTypes) rather than a
// copy here that could drift from it. This endpoint stays deliberately dumb about
// what those types mean.
func (h Handler) AgentActivity(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}

	actors := splitCSV(c.Query("actor_ids"))
	types := splitCSV(c.Query("event_types"))
	if len(actors) == 0 || len(types) == 0 {
		// Fail closed. A caller that meant to send filters and sent none gets an
		// error, not the org's entire event history.
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "actor_ids and event_types are both required and must be non-empty",
		})
	}

	limit := 200
	if n, err := strconv.Atoi(c.Query("limit")); err == nil && n > 0 && n <= 1000 {
		limit = n
	}

	// Time window. Defaults to the last 7 days so an unbounded scan is never the
	// accidental default on the largest table in the schema.
	since := time.Now().UTC().AddDate(0, 0, -7)
	if v := c.Query("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t.UTC()
		}
	}

	args := []any{orgID, since}
	sql := `
		SELECT event_id, trace_id, event_type, event_time, status, session_id, run_id,
		       actor_id, step_name, operation, latency_ms, error_message,
		       argMax(payload_json, ingested_at) AS payload_json
		FROM events_raw
		WHERE organization_id = ? AND event_time >= ?
		  AND actor_id IN (` + placeholders(len(actors)) + `)
		  AND event_type IN (` + placeholders(len(types)) + `)
		GROUP BY event_id, trace_id, event_type, event_time, status, session_id, run_id,
		         actor_id, step_name, operation, latency_ms, error_message
		ORDER BY event_time DESC
		LIMIT ?`
	for _, a := range actors {
		args = append(args, a)
	}
	for _, t := range types {
		args = append(args, t)
	}
	args = append(args, limit)

	rows, err := h.Store.DB.QueryContext(c.UserContext(), sql, args...)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	out := make([]fiber.Map, 0, limit)
	for rows.Next() {
		var (
			eventID, traceID, eventType, status string
			sessionID, runID, actorID, stepName string
			operation, errMessage, payload      string
			eventTime                           time.Time
			latencyMS                           int64
		)
		if err := rows.Scan(&eventID, &traceID, &eventType, &eventTime, &status, &sessionID,
			&runID, &actorID, &stepName, &operation, &latencyMS, &errMessage, &payload); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
		var p map[string]any
		if payload != "" {
			_ = json.Unmarshal([]byte(payload), &p)
		}
		out = append(out, fiber.Map{
			"event_id":   eventID,
			"trace_id":   traceID,
			"event_type": eventType,
			"event_time": eventTime,
			"status":     status,
			"game":       sessionID,
			"match_id":   runID,
			"agent_id":   actorID,
			"step_name":  stepName,
			"operation":  operation,
			"latency_ms": latencyMS,
			"error":      errMessage,
			"payload":    p,
		})
	}
	return c.JSON(fiber.Map{"events": out, "count": len(out)})
}

// placeholders builds "?,?,?" for an IN clause. The values themselves are always
// bound as parameters — never interpolated — so an agent id containing SQL is inert.
func placeholders(n int) string {
	if n == 0 {
		return "''" // unreachable (callers reject empty), but never emit a bare IN ().
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// splitCSV parses a comma-separated filter, dropping blanks so a trailing comma or a
// stray space cannot widen the filter to an empty-string match.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
