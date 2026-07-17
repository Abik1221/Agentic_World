package query

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
)

// caCostExpr prefers reconciled (billed) cost when available, else the estimate.
const caCostExpr = "if(reconciled_cost > 0, reconciled_cost, estimated_cost)"

// caModelCallWhere scopes a query to billable model-call rows for one org + window.
// Bind params, in order: organization_id, from, to.
const caModelCallWhere = `
	organization_id = ?
	AND event_type = 'model_call_completed'
	AND event_time >= ?
	AND event_time <= ?`

// caParseWindow resolves ?from / ?to (RFC3339 or ClickHouse-parsable strings),
// defaulting to the last 30 days. ClickHouse parses the strings directly.
func caParseWindow(c *fiber.Ctx) (from string, to string) {
	from = c.Query("from")
	to = c.Query("to")
	if to == "" {
		to = time.Now().UTC().Format("2006-01-02 15:04:05")
	}
	if from == "" {
		from = time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02 15:04:05")
	}
	return from, to
}

// CostAnalytics is GET /v1/cost-analytics — a deep dive into model spend.
// Returns per-run cost statistics (avg / median / p90 / p95 / min / max / range),
// a per-model breakdown (tokens + USD), a per-operation breakdown (by app_id),
// a per-archetype split, and a daily time series. All costs are USD.
func (h Handler) CostAnalytics(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	from, to := caParseWindow(c)
	ctx := c.UserContext()

	// ---- Per-run statistics --------------------------------------------------
	var runStats fiber.Map
	{
		row := h.Store.DB.QueryRowContext(ctx, `
			WITH run_cost AS (
				SELECT
					trace_id,
					sum(`+caCostExpr+`) AS usd,
					sum(total_tokens) AS toks,
					sum(prompt_tokens) AS ptok,
					sum(completion_tokens) AS ctok
				FROM events_raw
				WHERE `+caModelCallWhere+`
				GROUP BY trace_id
			)
			SELECT
				count(),
				coalesce(sum(usd), 0),
				coalesce(avg(usd), 0),
				coalesce(median(usd), 0),
				coalesce(quantile(0.9)(usd), 0),
				coalesce(quantile(0.95)(usd), 0),
				coalesce(min(usd), 0),
				coalesce(max(usd), 0),
				coalesce(avg(toks), 0),
				coalesce(median(toks), 0),
				coalesce(sum(toks), 0),
				coalesce(sum(ptok), 0),
				coalesce(sum(ctok), 0)
			FROM run_cost`, orgID, from, to)
		var runs, totalTokens, promptTokens, completionTokens int64
		var totalUSD, avgUSD, medUSD, p90USD, p95USD, minUSD, maxUSD, avgTokens, medTokens float64
		if err := row.Scan(&runs, &totalUSD, &avgUSD, &medUSD, &p90USD, &p95USD, &minUSD, &maxUSD,
			&avgTokens, &medTokens, &totalTokens, &promptTokens, &completionTokens); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		runStats = fiber.Map{
			"runs":              runs,
			"total_usd":         totalUSD,
			"avg_usd":           avgUSD,
			"median_usd":        medUSD,
			"p90_usd":           p90USD,
			"p95_usd":           p95USD,
			"min_usd":           minUSD,
			"max_usd":           maxUSD,
			"range_usd":         maxUSD - minUSD,
			"avg_tokens":        avgTokens,
			"median_tokens":     medTokens,
			"total_tokens":      totalTokens,
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
		}
	}

	// ---- Per-model breakdown -------------------------------------------------
	byModel := make([]fiber.Map, 0)
	{
		rows, err := h.Store.DB.QueryContext(ctx, `
			SELECT
				provider,
				model,
				count() AS calls,
				uniqExact(trace_id) AS runs,
				sum(`+caCostExpr+`) AS usd,
				sum(prompt_tokens) AS ptok,
				sum(completion_tokens) AS ctok,
				sum(cached_tokens) AS cachetok,
				sum(reasoning_tokens) AS rtok,
				sum(total_tokens) AS toks
			FROM events_raw
			WHERE `+caModelCallWhere+` AND model != ''
			GROUP BY provider, model
			ORDER BY usd DESC
			LIMIT 100`, orgID, from, to)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		defer rows.Close()
		for rows.Next() {
			var provider, model string
			var calls, runs, ptok, ctok, cachetok, rtok, toks int64
			var usd float64
			if err := rows.Scan(&provider, &model, &calls, &runs, &usd, &ptok, &ctok, &cachetok, &rtok, &toks); err != nil {
				return c.Status(500).JSON(fiber.Map{"error": err.Error()})
			}
			byModel = append(byModel, fiber.Map{
				"provider":           provider,
				"model":              model,
				"calls":              calls,
				"runs":               runs,
				"total_usd":          usd,
				"avg_usd_per_call":   caSafeDiv(usd, float64(calls)),
				"avg_usd_per_run":    caSafeDiv(usd, float64(runs)),
				"avg_tokens_per_run": caSafeDiv(float64(toks), float64(runs)),
				"prompt_tokens":      ptok,
				"completion_tokens":  ctok,
				"cached_tokens":      cachetok,
				"reasoning_tokens":   rtok,
				"total_tokens":       toks,
				"usd_per_1m_tokens":  caSafeDiv(usd, float64(toks)) * 1_000_000,
			})
		}
	}

	// ---- Per-operation (app_id) breakdown ------------------------------------
	byOperation, err := h.caCostGroup(ctx, orgID, from, to, "if(app_id = '', '(unattributed)', app_id)")
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	// ---- Daily time series ---------------------------------------------------
	daily := make([]fiber.Map, 0)
	{
		rows, err := h.Store.DB.QueryContext(ctx, `
			SELECT
				toDate(event_time) AS d,
				sum(`+caCostExpr+`) AS usd,
				sum(total_tokens) AS toks,
				uniqExact(trace_id) AS runs
			FROM events_raw
			WHERE `+caModelCallWhere+`
			GROUP BY d
			ORDER BY d ASC`, orgID, from, to)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		defer rows.Close()
		for rows.Next() {
			var d time.Time
			var toks, runs int64
			var usd float64
			if err := rows.Scan(&d, &usd, &toks, &runs); err != nil {
				return c.Status(500).JSON(fiber.Map{"error": err.Error()})
			}
			daily = append(daily, fiber.Map{
				"date":         d.Format("2006-01-02"),
				"total_usd":    usd,
				"total_tokens": toks,
				"runs":         runs,
			})
		}
	}

	return c.JSON(fiber.Map{
		"from":         from,
		"to":           to,
		"run_stats":    runStats,
		"by_model":     byModel,
		"by_operation": byOperation,
		"daily":        daily,
	})
}

// caCostGroup runs the cost/token breakdown for an arbitrary grouping expression.
func (h Handler) caCostGroup(ctx context.Context, orgID, from, to, groupExpr string) ([]fiber.Map, error) {
	out := make([]fiber.Map, 0)
	rows, err := h.Store.DB.QueryContext(ctx, `
		SELECT
			`+groupExpr+` AS grp,
			count() AS calls,
			uniqExact(trace_id) AS runs,
			sum(`+caCostExpr+`) AS usd,
			sum(total_tokens) AS toks
		FROM events_raw
		WHERE `+caModelCallWhere+`
		GROUP BY grp
		ORDER BY usd DESC
		LIMIT 100`, orgID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var grp string
		var calls, runs, toks int64
		var usd float64
		if err := rows.Scan(&grp, &calls, &runs, &usd, &toks); err != nil {
			return nil, err
		}
		out = append(out, fiber.Map{
			"name":               grp,
			"calls":              calls,
			"runs":               runs,
			"total_usd":          usd,
			"avg_usd_per_run":    caSafeDiv(usd, float64(runs)),
			"total_tokens":       toks,
			"avg_tokens_per_run": caSafeDiv(float64(toks), float64(runs)),
		})
	}
	return out, nil
}

func caSafeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}
