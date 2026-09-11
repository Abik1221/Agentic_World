package query

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

type matchListRow struct {
	MatchID     string    `json:"match_id"`
	Game        string    `json:"game"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at,omitempty"`
	EventCount  int64     `json:"event_count"`
	Agents      []string  `json:"agents"`
	WinnerAgent string    `json:"winner_agent,omitempty"`
	Bid         int64     `json:"bid"`
	Tokens      int64     `json:"tokens"`
	CostUSD     float64   `json:"cost_usd"`
	Decisions   int64     `json:"decisions"`
	ChatLines   int64     `json:"chat_lines"`
}

const matchIDExpr = `if(run_id != '', run_id, if(startsWith(trace_id, 'match_'), substring(trace_id, 7), ''))`

// MatchList is GET /v1/matches — every ingested arena match, newest first, paginated.
// Monopoly (withdrawn) is never returned. Optional ?game=goofspiel|mafia, ?status=finished|in_progress.
func (h Handler) MatchList(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	limit := parseIntBounded(c.Query("limit"), 50, 1, 200)
	offset := parseInt(c.Query("offset"), 0)
	if offset < 0 {
		offset = 0
	}
	game := strings.ToLower(strings.TrimSpace(c.Query("game")))
	if game == "monopoly" {
		return c.JSON(fiber.Map{"matches": []matchListRow{}, "total": 0, "limit": limit, "offset": offset})
	}
	status := strings.ToLower(strings.TrimSpace(c.Query("status")))

	where := `organization_id = ? AND (` + matchIDExpr + `) != ''` + monopolySQL
	having := ""
	if status == "finished" {
		having = " HAVING countIf(event_type = 'trace_completed') > 0"
	} else if status == "in_progress" {
		having = " HAVING countIf(event_type = 'trace_completed') = 0"
	}
	args := []any{orgID}
	if game != "" {
		if !isLiveGame(game) {
			return c.JSON(fiber.Map{"matches": []matchListRow{}, "total": 0, "limit": limit, "offset": offset})
		}
		// Prefixes: goofspiel m_, mafia mf_, monopoly mp_ (excluded). m_% would
		// also match mf_/mp_, so goofspiel is "m_" minus those two.
		where += ` AND (session_id = ? OR (session_id = '' AND (` + matchIDExpr + `) LIKE ?`
		prefix := "m\\_%"
		if game == "mafia" {
			where += `))`
			prefix = "mf\\_%"
			args = append(args, game, prefix)
		} else {
			where += ` AND (` + matchIDExpr + `) NOT LIKE 'mf\\_%' AND (` + matchIDExpr + `) NOT LIKE 'mp\\_%'))`
			args = append(args, game, prefix)
		}
	}

	countQ := `
		SELECT count() FROM (
			SELECT ` + matchIDExpr + ` AS match_id
			FROM events_raw
			WHERE ` + where + `
			GROUP BY match_id` + having + `
		)`
	var total uint64
	if err := h.Store.DB.QueryRowContext(c.UserContext(), countQ, args...).Scan(&total); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	listQ := `
		SELECT
			` + matchIDExpr + ` AS match_id,
			anyIf(session_id, session_id != '' AND session_id != 'monopoly') AS game,
			min(event_time) AS started_at,
			max(event_time) AS ended_at,
			count() AS event_count,
			arrayStringConcat(groupUniqArrayIf(actor_id, actor_id != ''), ',') AS agents,
			argMaxIf(actor_id, event_time, event_type = 'trace_completed') AS winner,
			max(if(event_type IN ('trace_started','trace_completed'), JSONExtractInt(payload_json, 'bid'), 0)) AS bid,
			sum(total_tokens) AS tokens,
			sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)) AS cost_usd,
			countIf(event_type = 'agent_decision') AS decisions,
			countIf(event_type = 'agent_said') AS chat_lines,
			toUInt8(countIf(event_type = 'trace_completed') > 0) AS finished
		FROM events_raw
		WHERE ` + where + `
		GROUP BY match_id` + having + `
		ORDER BY started_at DESC
		LIMIT ? OFFSET ?`
	listArgs := append(append([]any{}, args...), limit, offset)
	rows, err := h.Store.DB.QueryContext(c.UserContext(), listQ, listArgs...)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	out := make([]matchListRow, 0, limit)
	for rows.Next() {
		var row matchListRow
		var agentsCSV string
		var finished uint8
		var ended sql.NullTime
		if err := rows.Scan(
			&row.MatchID, &row.Game, &row.StartedAt, &ended, &row.EventCount, &agentsCSV,
			&row.WinnerAgent, &row.Bid, &row.Tokens, &row.CostUSD, &row.Decisions, &row.ChatLines, &finished,
		); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if ended.Valid {
			row.EndedAt = ended.Time
		}
		row.Game = gameFromMatchID(row.MatchID, row.Game)
		if !isLiveGame(row.Game) || isMonopolyID(row.MatchID) {
			continue
		}
		if finished > 0 {
			row.Status = "finished"
		} else {
			row.Status = "in_progress"
		}
		if agentsCSV != "" {
			row.Agents = uniqueNonEmpty(strings.Split(agentsCSV, ","))
		}
		out = append(out, row)
	}

	return c.JSON(fiber.Map{
		"matches": out,
		"total":   total,
		"limit":   limit,
		"offset":  offset,
	})
}

func uniqueNonEmpty(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

type matchCostAgent struct {
	AgentID         string  `json:"agent_id"`
	Provider        string  `json:"provider"`
	Model           string  `json:"model"`
	MeterSource     string  `json:"meter_source,omitempty"`
	Calls           int64   `json:"calls"`
	PromptTokens    int64   `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	CostUSD         float64 `json:"cost_usd"`
	AvgLatencyMS    float64 `json:"avg_latency_ms"`
}

// MatchCost is GET /v1/matches/:id/cost — per-agent, per-model spend for one match.
func (h Handler) MatchCost(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	id := c.Params("match_id")
	if isMonopolyID(id) {
		return c.JSON(fiber.Map{"match_id": id, "agents": []matchCostAgent{}, "total_tokens": 0, "total_cost_usd": 0})
	}

	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT
			actor_id,
			if(provider = '', 'unknown', provider) AS provider,
			if(model = '', 'unknown', model) AS model,
			if(meter_source = '', 'sdk', meter_source) AS meter_source,
			count() AS calls,
			sum(prompt_tokens) AS prompt_tokens,
			sum(completion_tokens) AS completion_tokens,
			sum(reasoning_tokens) AS reasoning_tokens,
			sum(total_tokens) AS total_tokens,
			sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)) AS cost_usd,
			avg(latency_ms) AS avg_latency_ms
		FROM events_raw
		WHERE organization_id = ?
		  AND event_type = 'model_call_completed'
		  AND (run_id = ? OR trace_id = ? OR trace_id = concat('match_', ?))
		`+monopolySQL+`
		GROUP BY actor_id, provider, model, meter_source
		ORDER BY cost_usd DESC, total_tokens DESC
		LIMIT 200`, orgID, id, id, id)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	agents := make([]matchCostAgent, 0)
	var totalTok int64
	var totalCost float64
	for rows.Next() {
		var a matchCostAgent
		if err := rows.Scan(&a.AgentID, &a.Provider, &a.Model, &a.MeterSource, &a.Calls,
			&a.PromptTokens, &a.CompletionTokens, &a.ReasoningTokens, &a.TotalTokens, &a.CostUSD, &a.AvgLatencyMS); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		agents = append(agents, a)
		totalTok += a.TotalTokens
		totalCost += a.CostUSD
	}

	return c.JSON(fiber.Map{
		"match_id":        id,
		"agents":          agents,
		"total_tokens":    totalTok,
		"total_cost_usd":  totalCost,
	})
}

type matchLogRow struct {
	EventID   string         `json:"event_id"`
	Type      string         `json:"type"`
	At        time.Time      `json:"at"`
	Status    string         `json:"status"`
	AgentID   string         `json:"agent_id,omitempty"`
	Game      string         `json:"game,omitempty"`
	Provider  string         `json:"provider,omitempty"`
	Model     string         `json:"model,omitempty"`
	Tokens    int64          `json:"tokens,omitempty"`
	CostUSD   float64        `json:"cost_usd,omitempty"`
	LatencyMS int64          `json:"latency_ms,omitempty"`
	Error     string         `json:"error,omitempty"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// MatchLogs is GET /v1/matches/:id/logs — full chronological event stream for one match.
func (h Handler) MatchLogs(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	id := c.Params("match_id")
	if isMonopolyID(id) {
		return c.JSON(fiber.Map{"match_id": id, "entries": []matchLogRow{}, "count": 0})
	}
	limit := parseIntBounded(c.Query("limit"), 2000, 1, 5000)
	offset := parseInt(c.Query("offset"), 0)

	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT event_id, event_type, event_time, status, actor_id, session_id,
		       provider, model, total_tokens,
		       if(reconciled_cost > 0, reconciled_cost, estimated_cost) AS cost_usd,
		       latency_ms, error_message,
		       argMax(payload_json, ingested_at) AS payload_json
		FROM events_raw
		WHERE organization_id = ?
		  AND (run_id = ? OR trace_id = ? OR trace_id = concat('match_', ?))
		`+monopolySQL+`
		GROUP BY event_id, event_type, event_time, status, actor_id, session_id,
		         provider, model, total_tokens, cost_usd, latency_ms, error_message
		ORDER BY event_time ASC
		LIMIT ? OFFSET ?`, orgID, id, id, id, limit, offset)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	out := make([]matchLogRow, 0)
	for rows.Next() {
		var e matchLogRow
		var payload string
		if err := rows.Scan(&e.EventID, &e.Type, &e.At, &e.Status, &e.AgentID, &e.Game,
			&e.Provider, &e.Model, &e.Tokens, &e.CostUSD, &e.LatencyMS, &e.Error, &payload); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		if payload != "" {
			_ = json.Unmarshal([]byte(payload), &e.Detail)
		}
		out = append(out, e)
	}
	return c.JSON(fiber.Map{"match_id": id, "entries": out, "count": len(out), "limit": limit, "offset": offset})
}

type matchMoneySeat struct {
	AgentID    string `json:"agent_id"`
	Seat       int    `json:"seat"`
	Score      int    `json:"score"`
	CoinsDelta int64  `json:"coins_delta"`
}

// MatchMoney is GET /v1/matches/:id/money — stake, winner, per-seat distribution
// from the match.finished payload ingested as trace_completed.
func (h Handler) MatchMoney(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	id := c.Params("match_id")
	if isMonopolyID(id) {
		return c.JSON(fiber.Map{"match_id": id, "available": false})
	}

	row := h.Store.DB.QueryRowContext(c.UserContext(), `
		SELECT argMax(payload_json, ingested_at), argMax(actor_id, ingested_at)
		FROM events_raw
		WHERE organization_id = ?
		  AND event_type = 'trace_completed'
		  AND (run_id = ? OR trace_id = ? OR trace_id = concat('match_', ?))
		`+monopolySQL, orgID, id, id, id)
	var payload, actor string
	if err := row.Scan(&payload, &actor); err != nil && err != sql.ErrNoRows {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	if payload == "" {
		// Fall back to trace_started bid if the match never settled.
		start := h.Store.DB.QueryRowContext(c.UserContext(), `
			SELECT argMax(payload_json, ingested_at)
			FROM events_raw
			WHERE organization_id = ?
			  AND event_type = 'trace_started'
			  AND (run_id = ? OR trace_id = ? OR trace_id = concat('match_', ?))
			`+monopolySQL, orgID, id, id, id)
		var started string
		_ = start.Scan(&started)
		bid := jsonInt(started, "bid")
		return c.JSON(fiber.Map{
			"match_id":  id,
			"available": started != "",
			"settled":   false,
			"bid":       bid,
			"winner_agent": "",
			"seats":     []matchMoneySeat{},
		})
	}

	var body struct {
		MatchID     string           `json:"match_id"`
		Game        string           `json:"game"`
		WinnerAgent string           `json:"winner_agent"`
		Bid         int64            `json:"bid"`
		RakePct     int              `json:"rake_pct"`
		Pool        int64            `json:"pool"`
		Seats       []matchMoneySeat `json:"seats"`
	}
	_ = json.Unmarshal([]byte(payload), &body)
	if body.WinnerAgent == "" {
		body.WinnerAgent = actor
	}
	if body.MatchID == "" {
		body.MatchID = id
	}
	rakeCoins := int64(0)
	if body.Pool > 0 && body.RakePct > 0 {
		rakeCoins = body.Pool * int64(body.RakePct) / 100
	}
	return c.JSON(fiber.Map{
		"match_id":      body.MatchID,
		"game":          body.Game,
		"available":     true,
		"settled":       true,
		"bid":           body.Bid,
		"rake_pct":      body.RakePct,
		"pool":          body.Pool,
		"rake_coins":    rakeCoins,
		"winner_agent":  body.WinnerAgent,
		"seats":         body.Seats,
	})
}

func jsonInt(raw, key string) int64 {
	if raw == "" {
		return 0
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}
