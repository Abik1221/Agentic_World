package query

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/agent-arena/pyyol-lens/backend/internal/config"
	"github.com/agent-arena/pyyol-lens/backend/internal/schema"
	"github.com/agent-arena/pyyol-lens/backend/internal/store"
	"github.com/gofiber/fiber/v2"
)

type Handler struct {
	Config config.Config
	Store  *store.Store
}

func (h Handler) Traces(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	limit := parseInt(c.Query("limit"), 100)
	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT
			t.trace_id,
			t.request_id,
			t.status,
			t.organization_id,
			t.project_id,
			t.environment,
			t.user_id,
			t.started_at,
			t.ended_at,
			t.latency_ms,
			coalesce(e.event_count, 0) AS event_count,
			t.total_tokens,
			if(t.total_cost > 0, t.total_cost, greatest(coalesce(tus.cost_sum, toFloat64(0)), coalesce(ss.cost_sum, toFloat64(0)))) AS total_cost,
			t.error_type,
			t.error_message,
			coalesce(sm.primary_model, '') as model,
			coalesce(tc.primary_tool, '') as tool_name
		FROM (
			-- traces is a ReplacingMergeTree whose ORDER BY key includes started_at, a
			-- value foldTrace keeps rewriting as events arrive; out-of-order events
			-- therefore leave several un-collapsible rows per trace. Collapse to exactly
			-- one row per trace_id here (latest wins by updated_at) so the list, and the
			-- joins hanging off it, never multiply a trace.
			SELECT
				tr.trace_id AS trace_id,
				argMax(tr.request_id, tr.updated_at)      AS request_id,
				argMax(tr.status, tr.updated_at)          AS status,
				argMax(tr.organization_id, tr.updated_at) AS organization_id,
				argMax(tr.project_id, tr.updated_at)      AS project_id,
				argMax(tr.environment, tr.updated_at)     AS environment,
				argMax(tr.user_id, tr.updated_at)         AS user_id,
				min(tr.started_at)                        AS started_at,
				max(tr.ended_at)                          AS ended_at,
				argMax(tr.latency_ms, tr.updated_at)      AS latency_ms,
				argMax(tr.total_tokens, tr.updated_at)    AS total_tokens,
				argMax(tr.total_cost, tr.updated_at)      AS total_cost,
				argMax(tr.error_type, tr.updated_at)      AS error_type,
				argMax(tr.error_message, tr.updated_at)   AS error_message
			FROM traces AS tr
			WHERE tr.organization_id = ? AND tr.trace_id != ''
			GROUP BY tr.trace_id
		) t
		LEFT JOIN (
			SELECT trace_id, count() AS event_count
			FROM events
			WHERE trace_id IN (SELECT trace_id FROM traces WHERE organization_id = ?)
			GROUP BY trace_id
		) e ON e.trace_id = t.trace_id
		LEFT JOIN (
			SELECT trace_id, sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)) AS cost_sum
			FROM token_usage
			GROUP BY trace_id
		) tus ON tus.trace_id = t.trace_id
		LEFT JOIN (
			SELECT trace_id, sum(total_cost) AS cost_sum
			FROM spans
			GROUP BY trace_id
		) ss ON ss.trace_id = t.trace_id
		LEFT JOIN (
			SELECT trace_id, argMax(model, updated_at) AS primary_model
			FROM spans
			WHERE model != ''
			GROUP BY trace_id
		) sm ON sm.trace_id = t.trace_id
		LEFT JOIN (
			SELECT trace_id, argMax(tool_name, updated_at) AS primary_tool
			FROM spans
			WHERE tool_name != ''
			GROUP BY trace_id
		) tc ON tc.trace_id = t.trace_id
		WHERE t.trace_id != ''
		ORDER BY t.started_at DESC
		LIMIT ?`, orgID, orgID, limit)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	out := make([]schema.TraceSummary, 0)
	for rows.Next() {
		var item schema.TraceSummary
		if err := rows.Scan(
			&item.TraceID,
			&item.RequestID,
			&item.Status,
			&item.OrganizationID,
			&item.ProjectID,
			&item.Environment,
			&item.UserID,
			&item.StartedAt,
			&item.EndedAt,
			&item.LatencyMS,
			&item.EventCount,
			&item.TotalTokens,
			&item.TotalCost,
			&item.ErrorType,
			&item.ErrorMessage,
			&item.Model,
			&item.ToolName,
		); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		out = append(out, item)
	}
	return c.JSON(out)
}

func (h Handler) TraceByID(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	traceID := c.Params("trace_id")
	row := h.Store.DB.QueryRowContext(c.UserContext(), `
		SELECT
			t.trace_id,
			t.request_id,
			t.status,
			t.organization_id,
			t.project_id,
			t.environment,
			t.user_id,
			t.started_at,
			t.ended_at,
			t.latency_ms,
			coalesce(e.event_count, 0) AS event_count,
			t.total_tokens,
			if(t.total_cost > 0, t.total_cost, greatest(coalesce(tus.cost_sum, toFloat64(0)), coalesce(ss.cost_sum, toFloat64(0)))) AS total_cost,
			t.error_type,
			t.error_message,
			coalesce(sm.primary_model, '') as model,
			coalesce(tc.primary_tool, '') as tool_name
		FROM traces t
		LEFT JOIN (
			SELECT trace_id, count() AS event_count
			FROM events
			WHERE trace_id IN (SELECT trace_id FROM traces WHERE organization_id = ?)
			GROUP BY trace_id
		) e ON e.trace_id = t.trace_id
		LEFT JOIN (
			SELECT trace_id, sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)) AS cost_sum
			FROM token_usage
			GROUP BY trace_id
		) tus ON tus.trace_id = t.trace_id
		LEFT JOIN (
			SELECT trace_id, sum(total_cost) AS cost_sum
			FROM spans
			GROUP BY trace_id
		) ss ON ss.trace_id = t.trace_id
		LEFT JOIN (
			SELECT trace_id, argMax(model, updated_at) AS primary_model
			FROM spans
			WHERE model != ''
			GROUP BY trace_id
		) sm ON sm.trace_id = t.trace_id
		LEFT JOIN (
			SELECT trace_id, argMax(tool_name, updated_at) AS primary_tool
			FROM spans
			WHERE tool_name != ''
			GROUP BY trace_id
		) tc ON tc.trace_id = t.trace_id
		WHERE t.trace_id = ? AND t.organization_id = ?
		ORDER BY t.updated_at DESC
		LIMIT 1`, orgID, traceID, orgID)

	var item schema.TraceSummary
	if err := row.Scan(
		&item.TraceID,
		&item.RequestID,
		&item.Status,
		&item.OrganizationID,
		&item.ProjectID,
		&item.Environment,
		&item.UserID,
		&item.StartedAt,
		&item.EndedAt,
		&item.LatencyMS,
		&item.EventCount,
		&item.TotalTokens,
		&item.TotalCost,
		&item.ErrorType,
		&item.ErrorMessage,
		&item.Model,
		&item.ToolName,
	); err != nil {
		if err == sql.ErrNoRows {
			return c.Status(404).JSON(fiber.Map{"error": "trace not found"})
		}
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(item)
}

func (h Handler) TraceTree(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	traceID := c.Params("trace_id")
	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT
			span_id,
			trace_id,
			parent_span_id,
			span_type,
			step_name,
			status,
			started_at,
			ended_at,
			latency_ms,
			provider,
			model,
			tool_name,
			total_tokens,
			total_cost,
			error_type,
			error_message,
			coalesce(last_event_type, ''),
			coalesce(task_kind, ''),
			coalesce(archetype, ''),
			coalesce(scope, ''),
			coalesce(subagent_id, ''),
			coalesce(artifact_ids_in_json, '[]'),
			coalesce(artifact_ids_out_json, '[]')
		FROM spans
		WHERE trace_id = ? AND span_id != '' AND trace_id IN (SELECT trace_id FROM traces WHERE organization_id = ?)
		ORDER BY started_at ASC`, traceID, orgID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	nodes := make([]*schema.SpanSummary, 0)
	index := make(map[string]*schema.SpanSummary)
	for rows.Next() {
		node := &schema.SpanSummary{}
		var artInJSON, artOutJSON string
		if err := rows.Scan(
			&node.SpanID,
			&node.TraceID,
			&node.ParentSpanID,
			&node.SpanType,
			&node.StepName,
			&node.Status,
			&node.StartedAt,
			&node.EndedAt,
			&node.LatencyMS,
			&node.Provider,
			&node.Model,
			&node.ToolName,
			&node.TotalTokens,
			&node.EstimatedCost,
			&node.ErrorType,
			&node.ErrorMessage,
			&node.EventType,
			&node.TaskKind,
			&node.Archetype,
			&node.Scope,
			&node.SubagentID,
			&artInJSON,
			&artOutJSON,
		); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		_ = json.Unmarshal([]byte(artInJSON), &node.ArtifactIDsIn)
		_ = json.Unmarshal([]byte(artOutJSON), &node.ArtifactIDsOut)
		nodes = append(nodes, node)
		index[node.SpanID] = node
	}

	roots := make([]*schema.SpanSummary, 0)
	for _, node := range nodes {
		if node.ParentSpanID == "" {
			roots = append(roots, node)
			continue
		}
		parent := index[node.ParentSpanID]
		if parent == nil {
			roots = append(roots, node)
			continue
		}
		parent.Children = append(parent.Children, node)
	}
	return c.JSON(roots)
}

func (h Handler) TraceEvents(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	traceID := c.Params("trace_id")
	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT
			e.event_id,e.trace_id,t.request_id,e.span_id,s.parent_span_id,e.event_type,e.event_time,0 as sequence_number,e.source_service,'' as schema_version,
			e.status,t.organization_id,t.project_id,t.environment,t.user_id,coalesce(r.actor_id, '') as actor_id,t.session_id,coalesce(r.run_id, '') as run_id,'' as conversation_id,'' as app_id,
			'' as queue_job_id,coalesce(r.component, '') as component,coalesce(r.operation, '') as operation,s.span_type,s.step_name,
			if(r.provider != '', r.provider, s.provider),if(r.model != '', r.model, s.model),s.model_version,s.tool_name,s.tool_version,
			'' as root_input_ref,'' as root_output_ref,e.payload_ref,
			coalesce(r.payload_json, '') as payload_json,'' as prompt_version_ids_json,'' as model_config_versions_json,
			coalesce(r.event_latency_ms, s.latency_ms, toInt64(0)) as latency_ms,
			0 as input_bytes,0 as output_bytes,
			coalesce(r.prompt_tokens, toInt64(0)),coalesce(r.completion_tokens, toInt64(0)),coalesce(r.cached_tokens, toInt64(0)),coalesce(r.reasoning_tokens, toInt64(0)),
			if(r.total_tokens > 0, r.total_tokens, s.total_tokens),
			if(r.estimated_cost > 0, r.estimated_cost, s.total_cost),toFloat64(0) as reconciled_cost,
			coalesce(r.currency, '') as currency,coalesce(r.pricing_version, '') as pricing_version,coalesce(r.meter_source, '') as meter_source,coalesce(r.agent_kind, '') as agent_kind,e.error_type,'' as error_code,e.error_message,'' as sampling_reason,'' as redaction_summary_json,e.event_time
		FROM events e
		LEFT JOIN traces t ON e.trace_id = t.trace_id
		LEFT JOIN spans s ON e.trace_id = s.trace_id AND e.span_id = s.span_id
		LEFT JOIN (
			SELECT
				event_id,
				trace_id,
				argMax(payload_json, ingested_at) AS payload_json,
				argMax(prompt_tokens, ingested_at) AS prompt_tokens,
				argMax(completion_tokens, ingested_at) AS completion_tokens,
				argMax(cached_tokens, ingested_at) AS cached_tokens,
				argMax(reasoning_tokens, ingested_at) AS reasoning_tokens,
				argMax(total_tokens, ingested_at) AS total_tokens,
				argMax(estimated_cost, ingested_at) AS estimated_cost,
				argMax(provider, ingested_at) AS provider,
				argMax(model, ingested_at) AS model,
				argMax(latency_ms, ingested_at) AS event_latency_ms,
				-- Identity + provenance. These were hardcoded empty on the read path
				-- even though events_raw carries them, so the Trace Inspector could not
				-- say WHICH agent produced a span, could not link a span to a match, and
				-- could not tell gateway-metered cost from agent self-reported cost —
				-- the platform's structural anti-cheat signal, invisible in its own UI.
				argMax(actor_id, ingested_at) AS actor_id,
				argMax(run_id, ingested_at) AS run_id,
				argMax(component, ingested_at) AS component,
				argMax(operation, ingested_at) AS operation,
				argMax(currency, ingested_at) AS currency,
				argMax(pricing_version, ingested_at) AS pricing_version,
				argMax(meter_source, ingested_at) AS meter_source,
				-- Whose traffic this span is (external / harness). Without it the Trace
				-- Inspector cannot separate a platform benchmark run from a developer's own
				-- calls, which share this ingest by design. '' means unknown (pre-007).
				argMax(agent_kind, ingested_at) AS agent_kind
			FROM events_raw
			WHERE trace_id = ?
			GROUP BY event_id, trace_id
		) r ON e.event_id = r.event_id AND e.trace_id = r.trace_id
		WHERE e.trace_id = ? AND t.organization_id = ?
		ORDER BY event_time ASC
		LIMIT ?`, traceID, traceID, orgID, h.Config.MaxEventsPerRead)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	events := make([]schema.TelemetryEvent, 0)
	for rows.Next() {
		var item schema.TelemetryEvent
		var payload, promptVersions, modelVersions, redaction string
		if err := rows.Scan(
			&item.EventID, &item.TraceID, &item.RequestID, &item.SpanID, &item.ParentSpanID, &item.EventType, &item.EventTime, &item.SequenceNumber,
			&item.SourceService, &item.SchemaVersion, &item.Status, &item.OrganizationID, &item.ProjectID, &item.Environment, &item.UserID,
			&item.ActorID, &item.SessionID, &item.RunID, &item.ConversationID, &item.AppID, &item.QueueJobID,
			&item.Component, &item.Operation, &item.SpanType, &item.StepName, &item.Provider, &item.Model, &item.ModelVersion, &item.ToolName,
			&item.ToolVersion, &item.RootInputRef, &item.RootOutputRef, &item.PayloadRef, &payload, &promptVersions, &modelVersions, &item.LatencyMS,
			&item.InputBytes, &item.OutputBytes, &item.PromptTokens, &item.CompletionTokens, &item.CachedTokens, &item.ReasoningTokens, &item.TotalTokens,
			&item.EstimatedCost, &item.ReconciledCost, &item.Currency, &item.PricingVersion, &item.MeterSource, &item.AgentKind, &item.ErrorType, &item.ErrorCode,
			&item.ErrorMessage, &item.SamplingReason, &redaction, &item.IngestedAt,
		); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		_ = json.Unmarshal([]byte(payload), &item.PayloadJSON)
		_ = json.Unmarshal([]byte(promptVersions), &item.PromptVersionIDs)
		_ = json.Unmarshal([]byte(modelVersions), &item.ModelConfigVersions)
		_ = json.Unmarshal([]byte(redaction), &item.RedactionSummary)
		schema.EnrichTelemetryExportFromPayload(&item)
		events = append(events, item)
	}
	return c.JSON(events)
}

func (h Handler) UsageSummary(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT project_id, environment, provider, model, sum(prompt_tokens), sum(completion_tokens), sum(total_tokens)
		FROM token_usage
		WHERE total_tokens > 0
			AND trace_id IN (SELECT trace_id FROM traces WHERE organization_id = ?)
		GROUP BY project_id, environment, provider, model
		ORDER BY sum(total_tokens) DESC
		LIMIT 200`, orgID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()
	out := make([]fiber.Map, 0)
	for rows.Next() {
		var projectID, environment, provider, model string
		var promptTokens, completionTokens, totalTokens int64
		if err := rows.Scan(&projectID, &environment, &provider, &model, &promptTokens, &completionTokens, &totalTokens); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		out = append(out, fiber.Map{
			"project_id":        projectID,
			"environment":       environment,
			"provider":          provider,
			"model":             model,
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      totalTokens,
		})
	}
	if len(out) == 0 {
		fb, err := h.Store.DB.QueryContext(c.UserContext(), `
			SELECT project_id, environment, sum(total_tokens)
			FROM traces
			WHERE organization_id = ? AND total_tokens > 0
			GROUP BY project_id, environment
			ORDER BY sum(total_tokens) DESC
			LIMIT 200`, orgID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		defer fb.Close()
		for fb.Next() {
			var projectID, environment string
			var totalTokens int64
			if err := fb.Scan(&projectID, &environment, &totalTokens); err != nil {
				return c.Status(500).JSON(fiber.Map{"error": err.Error()})
			}
			out = append(out, fiber.Map{
				"project_id":        projectID,
				"environment":       environment,
				"provider":          "—",
				"model":             "trace rollup (no per-model breakdown)",
				"prompt_tokens":     int64(0),
				"completion_tokens": int64(0),
				"total_tokens":      totalTokens,
			})
		}
	}
	return c.JSON(out)
}

func (h Handler) CostsSummary(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT project_id, environment, provider, model, sum(estimated_cost), sum(reconciled_cost), anyLast(currency)
		FROM token_usage
		WHERE estimated_cost > 0 OR reconciled_cost > 0
			AND trace_id IN (SELECT trace_id FROM traces WHERE organization_id = ?)
		GROUP BY project_id, environment, provider, model
		ORDER BY sum(estimated_cost) DESC
		LIMIT 200`, orgID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()
	out := make([]fiber.Map, 0)
	for rows.Next() {
		var projectID, environment, provider, model, currency string
		var estimatedCost, reconciledCost float64
		if err := rows.Scan(&projectID, &environment, &provider, &model, &estimatedCost, &reconciledCost, &currency); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		out = append(out, fiber.Map{
			"project_id":      projectID,
			"environment":     environment,
			"provider":        provider,
			"model":           model,
			"estimated_cost":  estimatedCost,
			"reconciled_cost": reconciledCost,
			"currency":        currency,
		})
	}
	if len(out) == 0 {
		fb, err := h.Store.DB.QueryContext(c.UserContext(), `
			SELECT project_id, environment, sum(total_cost), 'USD'
			FROM traces
			WHERE organization_id = ? AND total_cost > 0
			GROUP BY project_id, environment
			ORDER BY sum(total_cost) DESC
			LIMIT 200`, orgID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		defer fb.Close()
		for fb.Next() {
			var projectID, environment, currency string
			var cost float64
			if err := fb.Scan(&projectID, &environment, &cost, &currency); err != nil {
				return c.Status(500).JSON(fiber.Map{"error": err.Error()})
			}
			out = append(out, fiber.Map{
				"project_id":      projectID,
				"environment":     environment,
				"provider":        "—",
				"model":           "trace rollup",
				"estimated_cost":  cost,
				"reconciled_cost": float64(0),
				"currency":        currency,
			})
		}
	}
	return c.JSON(out)
}

func (h Handler) Retrievals(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT r.trace_id, r.span_id, r.step_name, r.status, r.error_message, r.event_time, r.event_time
		FROM retrievals r
		INNER JOIN traces t ON t.trace_id = r.trace_id
		WHERE t.organization_id = ?
		ORDER BY event_time DESC
		LIMIT 200`, orgID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()
	out := make([]fiber.Map, 0)
	for rows.Next() {
		var traceID, spanID, stepName, status, errorMessage string
		var startedAt, endedAt sql.NullTime
		if err := rows.Scan(&traceID, &spanID, &stepName, &status, &errorMessage, &startedAt, &endedAt); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		out = append(out, fiber.Map{
			"trace_id":      traceID,
			"span_id":       spanID,
			"step_name":     stepName,
			"status":        status,
			"error_message": errorMessage,
			"started_at":    startedAt.Time,
			"ended_at":      endedAt.Time,
		})
	}
	return c.JSON(out)
}

func (h Handler) ToolCalls(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT tc.trace_id, tc.span_id, tc.tool_name, tc.status, tc.error_message, sum(tc.total_tokens), sum(tc.estimated_cost)
		FROM tool_calls tc
		INNER JOIN traces t ON t.trace_id = tc.trace_id
		WHERE t.organization_id = ?
		GROUP BY tc.trace_id, tc.span_id, tc.tool_name, tc.status, tc.error_message
		ORDER BY sum(tc.estimated_cost) DESC, max(tc.event_time) DESC
		LIMIT 200`, orgID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()
	out := make([]fiber.Map, 0)
	for rows.Next() {
		var traceID, spanID, toolName, status, errorMessage string
		var totalTokens int64
		var estimatedCost float64
		if err := rows.Scan(&traceID, &spanID, &toolName, &status, &errorMessage, &totalTokens, &estimatedCost); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		out = append(out, fiber.Map{
			"trace_id":       traceID,
			"span_id":        spanID,
			"tool_name":      toolName,
			"status":         status,
			"error_message":  errorMessage,
			"total_tokens":   totalTokens,
			"estimated_cost": estimatedCost,
		})
	}
	return c.JSON(out)
}

func notShipped(c *fiber.Ctx, feature string) error {
	return c.Status(fiber.StatusNotImplemented).JSON(fiber.Map{
		"error":   "not_implemented",
		"feature": feature,
		"message": "This query-plane feature is not shipped in this release.",
	})
}

func (h Handler) Evaluations(c *fiber.Ctx) error {
	return notShipped(c, "evaluations")
}

func (h Handler) EvaluationByID(c *fiber.Ctx) error {
	return notShipped(c, "evaluations")
}

func (h Handler) Prompts(c *fiber.Ctx) error {
	return notShipped(c, "prompts")
}

func (h Handler) PromptVersions(c *fiber.Ctx) error {
	return notShipped(c, "prompt-versions")
}

func (h Handler) Replays(c *fiber.Ctx) error {
	return notShipped(c, "replays")
}

func (h Handler) ReplayByID(c *fiber.Ctx) error {
	return notShipped(c, "replays")
}

func (h Handler) SearchTraces(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	q := c.Query("q")
	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT
			t.trace_id,
			t.status,
			t.started_at,
			t.ended_at,
			coalesce(e.event_count, 0) AS event_count,
			t.total_tokens,
			if(t.total_cost > 0, t.total_cost, greatest(coalesce(tus.cost_sum, toFloat64(0)), coalesce(ss.cost_sum, toFloat64(0)))) AS total_cost
		FROM traces t
		LEFT JOIN (
			SELECT trace_id, count() AS event_count
			FROM events
			WHERE trace_id IN (SELECT trace_id FROM traces WHERE organization_id = ?)
			GROUP BY trace_id
		) e ON e.trace_id = t.trace_id
		LEFT JOIN (
			SELECT trace_id, sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)) AS cost_sum
			FROM token_usage
			GROUP BY trace_id
		) tus ON tus.trace_id = t.trace_id
		LEFT JOIN (
			SELECT trace_id, sum(total_cost) AS cost_sum
			FROM spans
			GROUP BY trace_id
		) ss ON ss.trace_id = t.trace_id
		WHERE t.organization_id = ? AND (
			positionCaseInsensitive(t.trace_id, ?) > 0
			OR positionCaseInsensitive(t.request_id, ?) > 0
			OR positionCaseInsensitive(t.error_message, ?) > 0
		)
		ORDER BY t.started_at DESC
		LIMIT 100`, orgID, orgID, q, q, q)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()
	out := make([]fiber.Map, 0)
	for rows.Next() {
		var traceID, status string
		var startedAt, endedAt sql.NullTime
		var eventCount, totalTokens int64
		var totalCost float64
		if err := rows.Scan(&traceID, &status, &startedAt, &endedAt, &eventCount, &totalTokens, &totalCost); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		out = append(out, fiber.Map{
			"trace_id":     traceID,
			"status":       status,
			"started_at":   startedAt.Time,
			"ended_at":     endedAt.Time,
			"event_count":  eventCount,
			"total_tokens": totalTokens,
			"total_cost":   totalCost,
		})
	}
	return c.JSON(out)
}

func (h Handler) SearchEvents(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	q := c.Query("q")
	// Honour a client-supplied limit for pagination, capped at the configured
	// safety ceiling so a caller can page but never ask for an unbounded scan.
	limit := parseInt(c.Query("limit"), h.Config.MaxEventsPerRead)
	if limit <= 0 || limit > h.Config.MaxEventsPerRead {
		limit = h.Config.MaxEventsPerRead
	}
	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT e.event_id,e.event_type,e.event_time,e.trace_id,e.span_id,e.status,e.error_message,s.model,s.tool_name
		FROM events e
		LEFT JOIN (
			-- spans is a ReplacingMergeTree; collapse to one row per (trace_id, span_id)
			-- so the join can never multiply an event into duplicate result rows.
			SELECT trace_id, span_id,
				argMax(model, updated_at)     AS model,
				argMax(tool_name, updated_at) AS tool_name
			FROM spans
			GROUP BY trace_id, span_id
		) s ON e.trace_id = s.trace_id AND e.span_id = s.span_id
		WHERE e.trace_id IN (SELECT trace_id FROM traces WHERE organization_id = ?)
			AND (
			positionCaseInsensitive(e.error_message, ?) > 0
			OR positionCaseInsensitive(e.event_type, ?) > 0
			OR positionCaseInsensitive(s.model, ?) > 0
			OR positionCaseInsensitive(s.tool_name, ?) > 0
			)
		ORDER BY e.event_time DESC
		LIMIT ?`, orgID, q, q, q, q, limit)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()
	out := make([]fiber.Map, 0)
	for rows.Next() {
		var eventID, eventType, traceID, spanID, status, errorMessage, model, toolName string
		var eventTime sql.NullTime
		if err := rows.Scan(&eventID, &eventType, &eventTime, &traceID, &spanID, &status, &errorMessage, &model, &toolName); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		out = append(out, fiber.Map{
			"event_id":      eventID,
			"event_type":    eventType,
			"event_time":    eventTime.Time,
			"trace_id":      traceID,
			"span_id":       spanID,
			"status":        status,
			"error_message": errorMessage,
			"model":         model,
			"tool_name":     toolName,
		})
	}
	return c.JSON(out)
}

func (h Handler) Overview(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	// Health must not count leftover error_message on a completed trace, nor
	// superseded ReplacingMergeTree versions. traces is ReplacingMergeTree(updated_at):
	// a fold that later succeeded still carries the earlier error_message, and
	// count() without collapsing versions inflated "errors" into the hundreds
	// while the pipeline was fine.
	//
	// `errors` is the last-24h TERMINAL failure count (status = 'error' after
	// collapsing). That is what a health card should answer. Lifetime terminal
	// failures and "has leftover text but did not fail" are returned separately
	// so nobody has to guess why the headline moved.
	row := h.Store.DB.QueryRowContext(c.UserContext(), `
		SELECT
			count(),
			countIf(status = 'error' AND started_at >= now() - INTERVAL 24 HOUR),
			countIf(status = 'error'),
			countIf(error_message != '' AND status != 'error'),
			coalesce(quantile(0.95)(latency_ms), 0),
			coalesce(sum(total_tokens), 0),
			coalesce(sum(total_cost), 0)
		FROM (
			SELECT
				argMax(status, updated_at) AS status,
				argMax(error_message, updated_at) AS error_message,
				min(started_at) AS started_at,
				argMax(latency_ms, updated_at) AS latency_ms,
				argMax(total_tokens, updated_at) AS total_tokens,
				argMax(total_cost, updated_at) AS total_cost
			FROM traces
			WHERE organization_id = ?
			GROUP BY trace_id
		)
		`, orgID)
	var traces, errors24h, errorsLifetime, errorsMessageOnly, totalTokens int64
	var p95Latency, totalCost float64
	if err := row.Scan(&traces, &errors24h, &errorsLifetime, &errorsMessageOnly, &p95Latency, &totalTokens, &totalCost); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	eventsRow := h.Store.DB.QueryRowContext(c.UserContext(),
		`SELECT count() FROM events WHERE trace_id IN (SELECT trace_id FROM traces WHERE organization_id = ?)`, orgID)
	var events int64
	if err := eventsRow.Scan(&events); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{
		"traces_total":         traces,
		"events_total":         events,
		"errors":               errors24h,
		"errors_24h":           errors24h,
		"errors_lifetime":      errorsLifetime,
		"errors_message_only":  errorsMessageOnly,
		"error_window":         "24h",
		"p95_latency_ms":       p95Latency,
		"total_tokens":         totalTokens,
		"estimated_cost":       totalCost,
	})
}

func (h Handler) Runs(c *fiber.Ctx) error {
	return h.Traces(c)
}

func (h Handler) RunByID(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	traceID := c.Params("run_id")
	row := h.Store.DB.QueryRowContext(c.UserContext(), `
		SELECT
			t.trace_id,
			t.request_id,
			t.status,
			t.organization_id,
			t.project_id,
			t.environment,
			t.user_id,
			t.started_at,
			t.ended_at,
			t.latency_ms,
			(SELECT count() FROM events WHERE trace_id = t.trace_id),
			t.total_tokens,
			if(t.total_cost > 0, t.total_cost, greatest(coalesce(tus.cost_sum, toFloat64(0)), coalesce(ss.cost_sum, toFloat64(0)))) AS total_cost,
			t.error_type,
			t.error_message,
			coalesce(sm.primary_model, '') as model,
			coalesce(tc.primary_tool, '') as tool_name
		FROM traces t
		LEFT JOIN (
			SELECT trace_id, sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)) AS cost_sum
			FROM token_usage
			GROUP BY trace_id
		) tus ON tus.trace_id = t.trace_id
		LEFT JOIN (
			SELECT trace_id, sum(total_cost) AS cost_sum
			FROM spans
			GROUP BY trace_id
		) ss ON ss.trace_id = t.trace_id
		LEFT JOIN (
			SELECT trace_id, argMax(model, updated_at) AS primary_model
			FROM spans
			WHERE model != ''
			GROUP BY trace_id
		) sm ON sm.trace_id = t.trace_id
		LEFT JOIN (
			SELECT trace_id, argMax(tool_name, updated_at) AS primary_tool
			FROM spans
			WHERE tool_name != ''
			GROUP BY trace_id
		) tc ON tc.trace_id = t.trace_id
		WHERE t.trace_id = ? AND t.organization_id = ?
		ORDER BY t.updated_at DESC
		LIMIT 1`, traceID, orgID)

	var item schema.TraceSummary
	if err := row.Scan(
		&item.TraceID,
		&item.RequestID,
		&item.Status,
		&item.OrganizationID,
		&item.ProjectID,
		&item.Environment,
		&item.UserID,
		&item.StartedAt,
		&item.EndedAt,
		&item.LatencyMS,
		&item.EventCount,
		&item.TotalTokens,
		&item.TotalCost,
		&item.ErrorType,
		&item.ErrorMessage,
		&item.Model,
		&item.ToolName,
	); err != nil {
		if err == sql.ErrNoRows {
			return c.Status(404).JSON(fiber.Map{"error": "run not found"})
		}
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(item)
}

func (h Handler) TraceTimeline(c *fiber.Ctx) error {
	return h.TraceEvents(c)
}

// MatchByID returns the per-match event stream (all events carrying this match's
// run_id), ordered in time — the "everything that happened in match X" view.
func (h Handler) MatchByID(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	id := c.Params("match_id")
	if isMonopolyID(id) {
		return c.JSON([]fiber.Map{})
	}
	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT me.trace_id, me.event_type, me.event_time, me.status, me.error_message
		FROM match_events me
		INNER JOIN traces t ON t.trace_id = me.trace_id
		WHERE me.run_id = ? AND t.organization_id = ?
		ORDER BY event_time ASC
		LIMIT ?`, id, orgID, h.Config.MaxEventsPerRead)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()
	out := make([]fiber.Map, 0)
	for rows.Next() {
		var traceID, eventType, status, errorMessage string
		var eventTime sql.NullTime
		if err := rows.Scan(&traceID, &eventType, &eventTime, &status, &errorMessage); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		out = append(out, fiber.Map{
			"trace_id":      traceID,
			"event_type":    eventType,
			"event_time":    eventTime.Time,
			"status":        status,
			"error_message": errorMessage,
		})
	}
	return c.JSON(out)
}

// MatchDecisions returns, per agent, the full per-move decision trail for a match
// — action, outcome, latency, agent reasoning, and token usage — plus the token
// totals. It reads the benchmark_recorded event's payload_json (which carries the
// decision_log the aggregate benchmark tables drop), keyed by run_id = match id.
func (h Handler) MatchDecisions(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	id := c.Params("match_id")
	if isMonopolyID(id) {
		return c.JSON(fiber.Map{"match_id": id, "agents": []fiber.Map{}})
	}
	// Accept either the bare match id (run_id) or the "match_<id>" trace id, so
	// links from the run/trace views resolve without prefix juggling.
	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT actor_id, session_id, argMax(payload_json, ingested_at) AS payload_json
		FROM events_raw
		WHERE organization_id = ? AND event_type = 'benchmark_recorded' AND (run_id = ? OR trace_id = ?)
		GROUP BY actor_id, session_id`, orgID, id, id)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()
	agents := make([]fiber.Map, 0)
	for rows.Next() {
		var actorID, session, payload string
		if err := rows.Scan(&actorID, &session, &payload); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		var p map[string]any
		if payload != "" {
			_ = json.Unmarshal([]byte(payload), &p)
		}
		agents = append(agents, fiber.Map{"agent_id": actorID, "game": session, "benchmark": p})
	}
	return c.JSON(fiber.Map{"match_id": id, "agents": agents})
}

func (h Handler) ProjectionStatus(c *fiber.Ctx) error {
	type countRow struct {
		Name  string `json:"name"`
		Count int64  `json:"count"`
	}
	type timeRow struct {
		Name  string  `json:"name"`
		Value *string `json:"value"`
	}
	check := func(name string, q string) (countRow, error) {
		row := h.Store.DB.QueryRowContext(c.UserContext(), q)
		var cnt int64
		if err := row.Scan(&cnt); err != nil {
			return countRow{}, err
		}
		return countRow{Name: name, Count: cnt}, nil
	}
	checkTime := func(name string, q string) (timeRow, error) {
		row := h.Store.DB.QueryRowContext(c.UserContext(), q)
		var ts sql.NullTime
		if err := row.Scan(&ts); err != nil {
			return timeRow{}, err
		}
		if !ts.Valid {
			return timeRow{Name: name, Value: nil}, nil
		}
		iso := ts.Time.UTC().Format("2006-01-02T15:04:05Z")
		return timeRow{Name: name, Value: &iso}, nil
	}
	windowMinutes := parseIntBounded(c.Query("window_minutes"), defaultHealthWindowMinutes, minHealthWindowMinutes, maxHealthWindowMinutes)
	degradedBacklogThreshold := parseInt64Min(c.Query("degraded_backlog"), defaultDegradedBacklogThreshold, 1)
	stalledBacklogThreshold := parseInt64Min(c.Query("stalled_backlog"), defaultStalledBacklogThreshold, degradedBacklogThreshold+1)
	degradedLagThresholdS := parseInt64Min(c.Query("degraded_lag_seconds"), defaultDegradedLagThresholdS, 1)
	stalledLagThresholdS := parseInt64Min(c.Query("stalled_lag_seconds"), defaultStalledLagThresholdS, degradedLagThresholdS+1)

	raw, err := check("events_raw", `SELECT count() FROM events_raw`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	traces, err := check("traces", `SELECT count() FROM traces`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	spans, err := check("spans", `SELECT count() FROM spans`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	events, err := check("events", `SELECT count() FROM events`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	usage, err := check("token_usage", `SELECT count() FROM token_usage`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	toolCalls, err := check("tool_calls", `SELECT count() FROM tool_calls`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	retrievals, err := check("retrievals", `SELECT count() FROM retrievals`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	matchEvents, err := check("match_events", `SELECT count() FROM match_events`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	processed, err := check("processed_events", `SELECT count() FROM processed_events`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	hourly, err := check("rollup_hourly", `SELECT count() FROM rollup_hourly`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	daily, err := check("rollup_daily", `SELECT count() FROM rollup_daily`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	monthly, err := check("rollup_monthly", `SELECT count() FROM rollup_monthly`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	rawLatest, err := checkTime("events_raw_latest_ts", `SELECT max(event_time) FROM events_raw`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	processedLatest, err := checkTime("processed_events_latest_at", `SELECT max(processed_at) FROM processed_events`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	rawRecentQuery := fmt.Sprintf("SELECT count() FROM events_raw WHERE event_time >= now() - INTERVAL %d MINUTE", windowMinutes)
	processedRecentQuery := fmt.Sprintf("SELECT count() FROM processed_events WHERE processed_at >= now() - INTERVAL %d MINUTE", windowMinutes)
	rawRecentLabel := fmt.Sprintf("events_raw_last_%dm", windowMinutes)
	processedRecentLabel := fmt.Sprintf("processed_events_last_%dm", windowMinutes)
	rawLast5m, err := check(rawRecentLabel, rawRecentQuery)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	processedLast5m, err := check(processedRecentLabel, processedRecentQuery)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	estimatedBacklog := raw.Count - processed.Count
	if estimatedBacklog < 0 {
		estimatedBacklog = 0
	}
	var lagSeconds *int64
	if rawLatest.Value != nil && processedLatest.Value != nil {
		rawTs, parseRawErr := time.Parse(time.RFC3339, *rawLatest.Value)
		processedTs, parseProcessedErr := time.Parse(time.RFC3339, *processedLatest.Value)
		if parseRawErr == nil && parseProcessedErr == nil {
			seconds := int64(rawTs.Sub(processedTs).Seconds())
			if seconds < 0 {
				seconds = 0
			}
			lagSeconds = &seconds
		}
	}
	pipelineStatus, statusReason := classifyPipelineHealth(raw.Count, processed.Count, rawLast5m.Count, processedLast5m.Count, estimatedBacklog, lagSeconds, windowMinutes, degradedBacklogThreshold, stalledBacklogThreshold, degradedLagThresholdS, stalledLagThresholdS)

	return c.JSON(fiber.Map{
		"counts": []countRow{
			raw,
			traces,
			spans,
			events,
			usage,
			toolCalls,
			retrievals,
			matchEvents,
			processed,
			hourly,
			daily,
			monthly,
			rawLast5m,
			processedLast5m,
		},
		"timestamps": []timeRow{
			rawLatest,
			processedLatest,
		},
		"pipeline": fiber.Map{
			"estimated_backlog_events": estimatedBacklog,
			"estimated_lag_seconds":    lagSeconds,
			"status":                   pipelineStatus,
			"status_reason":            statusReason,
			"thresholds": fiber.Map{
				"degraded_backlog_events": degradedBacklogThreshold,
				"stalled_backlog_events":  stalledBacklogThreshold,
				"degraded_lag_seconds":    degradedLagThresholdS,
				"stalled_lag_seconds":     stalledLagThresholdS,
				"window_minutes":          windowMinutes,
			},
		},
		"ready": map[string]bool{
			"has_raw_events":            raw.Count > 0,
			"has_trace_projection":      traces.Count > 0 || raw.Count == 0,
			"has_span_projection":       spans.Count > 0 || raw.Count == 0,
			"has_event_projection":      events.Count > 0 || raw.Count == 0,
			"has_usage_projection":      usage.Count > 0 || raw.Count == 0,
			"has_rollup_projection":     daily.Count > 0 || raw.Count == 0,
			"has_processed_event_index": processed.Count > 0 || raw.Count == 0,
		},
	})
}

func (h Handler) ProjectionStatusSummary(c *fiber.Ctx) error {
	check := func(q string) (int64, error) {
		row := h.Store.DB.QueryRowContext(c.UserContext(), q)
		var cnt int64
		if err := row.Scan(&cnt); err != nil {
			return 0, err
		}
		return cnt, nil
	}
	checkTime := func(q string) (*string, error) {
		row := h.Store.DB.QueryRowContext(c.UserContext(), q)
		var ts sql.NullTime
		if err := row.Scan(&ts); err != nil {
			return nil, err
		}
		if !ts.Valid {
			return nil, nil
		}
		iso := ts.Time.UTC().Format("2006-01-02T15:04:05Z")
		return &iso, nil
	}
	windowMinutes := parseIntBounded(c.Query("window_minutes"), defaultHealthWindowMinutes, minHealthWindowMinutes, maxHealthWindowMinutes)
	degradedBacklogThreshold := parseInt64Min(c.Query("degraded_backlog"), defaultDegradedBacklogThreshold, 1)
	stalledBacklogThreshold := parseInt64Min(c.Query("stalled_backlog"), defaultStalledBacklogThreshold, degradedBacklogThreshold+1)
	degradedLagThresholdS := parseInt64Min(c.Query("degraded_lag_seconds"), defaultDegradedLagThresholdS, 1)
	stalledLagThresholdS := parseInt64Min(c.Query("stalled_lag_seconds"), defaultStalledLagThresholdS, degradedLagThresholdS+1)

	rawCount, err := check(`SELECT count() FROM events_raw`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	processedCount, err := check(`SELECT count() FROM processed_events`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	rawRecentQuery := fmt.Sprintf("SELECT count() FROM events_raw WHERE event_time >= now() - INTERVAL %d MINUTE", windowMinutes)
	processedRecentQuery := fmt.Sprintf("SELECT count() FROM processed_events WHERE processed_at >= now() - INTERVAL %d MINUTE", windowMinutes)
	rawLast5m, err := check(rawRecentQuery)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	processedLast5m, err := check(processedRecentQuery)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	rawLatest, err := checkTime(`SELECT max(event_time) FROM events_raw`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	processedLatest, err := checkTime(`SELECT max(processed_at) FROM processed_events`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	estimatedBacklog := rawCount - processedCount
	if estimatedBacklog < 0 {
		estimatedBacklog = 0
	}
	var lagSeconds *int64
	if rawLatest != nil && processedLatest != nil {
		rawTs, parseRawErr := time.Parse(time.RFC3339, *rawLatest)
		processedTs, parseProcessedErr := time.Parse(time.RFC3339, *processedLatest)
		if parseRawErr == nil && parseProcessedErr == nil {
			seconds := int64(rawTs.Sub(processedTs).Seconds())
			if seconds < 0 {
				seconds = 0
			}
			lagSeconds = &seconds
		}
	}
	status, statusReason := classifyPipelineHealth(rawCount, processedCount, rawLast5m, processedLast5m, estimatedBacklog, lagSeconds, windowMinutes, degradedBacklogThreshold, stalledBacklogThreshold, degradedLagThresholdS, stalledLagThresholdS)

	return c.JSON(fiber.Map{
		"status":        status,
		"status_reason": statusReason,
		"counters": fiber.Map{
			"events_raw_total":         rawCount,
			"processed_events_total":   processedCount,
			"events_raw_last_5m":       rawLast5m,
			"processed_events_last_5m": processedLast5m,
			"estimated_backlog_events": estimatedBacklog,
			"estimated_lag_seconds":    lagSeconds,
			"window_minutes":           windowMinutes,
		},
	})
}

func (h Handler) ProjectionMetrics(c *fiber.Ctx) error {
	check := func(q string) (int64, error) {
		row := h.Store.DB.QueryRowContext(c.UserContext(), q)
		var cnt int64
		if err := row.Scan(&cnt); err != nil {
			return 0, err
		}
		return cnt, nil
	}
	checkTime := func(q string) (*string, error) {
		row := h.Store.DB.QueryRowContext(c.UserContext(), q)
		var ts sql.NullTime
		if err := row.Scan(&ts); err != nil {
			return nil, err
		}
		if !ts.Valid {
			return nil, nil
		}
		iso := ts.Time.UTC().Format("2006-01-02T15:04:05Z")
		return &iso, nil
	}
	windowMinutes := parseIntBounded(c.Query("window_minutes"), defaultHealthWindowMinutes, minHealthWindowMinutes, maxHealthWindowMinutes)
	degradedBacklogThreshold := parseInt64Min(c.Query("degraded_backlog"), defaultDegradedBacklogThreshold, 1)
	stalledBacklogThreshold := parseInt64Min(c.Query("stalled_backlog"), defaultStalledBacklogThreshold, degradedBacklogThreshold+1)
	degradedLagThresholdS := parseInt64Min(c.Query("degraded_lag_seconds"), defaultDegradedLagThresholdS, 1)
	stalledLagThresholdS := parseInt64Min(c.Query("stalled_lag_seconds"), defaultStalledLagThresholdS, degradedLagThresholdS+1)

	rawTotal, err := check(`SELECT count() FROM events_raw`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	processedTotal, err := check(`SELECT count() FROM processed_events`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	rawRecentQuery := fmt.Sprintf("SELECT count() FROM events_raw WHERE event_time >= now() - INTERVAL %d MINUTE", windowMinutes)
	processedRecentQuery := fmt.Sprintf("SELECT count() FROM processed_events WHERE processed_at >= now() - INTERVAL %d MINUTE", windowMinutes)
	rawLast5m, err := check(rawRecentQuery)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	processedLast5m, err := check(processedRecentQuery)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	tracesTotal, err := check(`SELECT count() FROM traces`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	spansTotal, err := check(`SELECT count() FROM spans`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	eventsTotal, err := check(`SELECT count() FROM events`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	usageTotal, err := check(`SELECT count() FROM token_usage`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	rollupDailyTotal, err := check(`SELECT count() FROM rollup_daily`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	rawLatest, err := checkTime(`SELECT max(event_time) FROM events_raw`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	processedLatest, err := checkTime(`SELECT max(processed_at) FROM processed_events`)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	estimatedBacklog := rawTotal - processedTotal
	if estimatedBacklog < 0 {
		estimatedBacklog = 0
	}
	var lagSeconds int64
	if rawLatest != nil && processedLatest != nil {
		rawTs, parseRawErr := time.Parse(time.RFC3339, *rawLatest)
		processedTs, parseProcessedErr := time.Parse(time.RFC3339, *processedLatest)
		if parseRawErr == nil && parseProcessedErr == nil {
			seconds := int64(rawTs.Sub(processedTs).Seconds())
			if seconds > 0 {
				lagSeconds = seconds
			}
		}
	}

	status, _ := classifyPipelineHealth(rawTotal, processedTotal, rawLast5m, processedLast5m, estimatedBacklog, &lagSeconds, windowMinutes, degradedBacklogThreshold, stalledBacklogThreshold, degradedLagThresholdS, stalledLagThresholdS)
	statusCode := int64(0)
	if status == "degraded" {
		statusCode = 1
	} else if status == "stalled" {
		statusCode = 2
	}

	return c.JSON(fiber.Map{
		"pipeline_status_code":       statusCode,
		"events_raw_total":           rawTotal,
		"processed_events_total":     processedTotal,
		"events_raw_last_5m":         rawLast5m,
		"processed_events_last_5m":   processedLast5m,
		"estimated_backlog_events":   estimatedBacklog,
		"estimated_lag_seconds":      lagSeconds,
		"traces_total":               tracesTotal,
		"spans_total":                spansTotal,
		"events_total":               eventsTotal,
		"token_usage_total":          usageTotal,
		"rollup_daily_total":         rollupDailyTotal,
		"degraded_backlog_threshold": degradedBacklogThreshold,
		"stalled_backlog_threshold":  stalledBacklogThreshold,
		"degraded_lag_threshold_s":   degradedLagThresholdS,
		"stalled_lag_threshold_s":    stalledLagThresholdS,
		"window_minutes":             int64(windowMinutes),
	})
}

func (h Handler) ProjectionMetricsPrometheus(c *fiber.Ctx) error {
	check := func(q string) (int64, error) {
		row := h.Store.DB.QueryRowContext(c.UserContext(), q)
		var cnt int64
		if err := row.Scan(&cnt); err != nil {
			return 0, err
		}
		return cnt, nil
	}
	checkTime := func(q string) (*string, error) {
		row := h.Store.DB.QueryRowContext(c.UserContext(), q)
		var ts sql.NullTime
		if err := row.Scan(&ts); err != nil {
			return nil, err
		}
		if !ts.Valid {
			return nil, nil
		}
		iso := ts.Time.UTC().Format(time.RFC3339)
		return &iso, nil
	}

	windowMinutes := parseIntBounded(c.Query("window_minutes"), defaultHealthWindowMinutes, minHealthWindowMinutes, maxHealthWindowMinutes)
	degradedBacklogThreshold := parseInt64Min(c.Query("degraded_backlog"), defaultDegradedBacklogThreshold, 1)
	stalledBacklogThreshold := parseInt64Min(c.Query("stalled_backlog"), defaultStalledBacklogThreshold, degradedBacklogThreshold+1)
	degradedLagThresholdS := parseInt64Min(c.Query("degraded_lag_seconds"), defaultDegradedLagThresholdS, 1)
	stalledLagThresholdS := parseInt64Min(c.Query("stalled_lag_seconds"), defaultStalledLagThresholdS, degradedLagThresholdS+1)

	rawTotal, err := check(`SELECT count() FROM events_raw`)
	if err != nil {
		return c.Status(500).SendString("# error querying events_raw_total\n")
	}
	processedTotal, err := check(`SELECT count() FROM processed_events`)
	if err != nil {
		return c.Status(500).SendString("# error querying processed_events_total\n")
	}
	rawRecentQuery := fmt.Sprintf("SELECT count() FROM events_raw WHERE event_time >= now() - INTERVAL %d MINUTE", windowMinutes)
	processedRecentQuery := fmt.Sprintf("SELECT count() FROM processed_events WHERE processed_at >= now() - INTERVAL %d MINUTE", windowMinutes)
	rawRecent, err := check(rawRecentQuery)
	if err != nil {
		return c.Status(500).SendString("# error querying events_raw_recent\n")
	}
	processedRecent, err := check(processedRecentQuery)
	if err != nil {
		return c.Status(500).SendString("# error querying processed_events_recent\n")
	}
	tracesTotal, err := check(`SELECT count() FROM traces`)
	if err != nil {
		return c.Status(500).SendString("# error querying traces_total\n")
	}
	spansTotal, err := check(`SELECT count() FROM spans`)
	if err != nil {
		return c.Status(500).SendString("# error querying spans_total\n")
	}
	eventsTotal, err := check(`SELECT count() FROM events`)
	if err != nil {
		return c.Status(500).SendString("# error querying events_total\n")
	}
	usageTotal, err := check(`SELECT count() FROM token_usage`)
	if err != nil {
		return c.Status(500).SendString("# error querying token_usage_total\n")
	}
	rollupDailyTotal, err := check(`SELECT count() FROM rollup_daily`)
	if err != nil {
		return c.Status(500).SendString("# error querying rollup_daily_total\n")
	}

	rawLatest, err := checkTime(`SELECT max(event_time) FROM events_raw`)
	if err != nil {
		return c.Status(500).SendString("# error querying events_raw_latest_ts\n")
	}
	processedLatest, err := checkTime(`SELECT max(processed_at) FROM processed_events`)
	if err != nil {
		return c.Status(500).SendString("# error querying processed_events_latest_ts\n")
	}

	estimatedBacklog := rawTotal - processedTotal
	if estimatedBacklog < 0 {
		estimatedBacklog = 0
	}
	var lagSeconds int64
	if rawLatest != nil && processedLatest != nil {
		rawTs, parseRawErr := time.Parse(time.RFC3339, *rawLatest)
		processedTs, parseProcessedErr := time.Parse(time.RFC3339, *processedLatest)
		if parseRawErr == nil && parseProcessedErr == nil {
			seconds := int64(rawTs.Sub(processedTs).Seconds())
			if seconds > 0 {
				lagSeconds = seconds
			}
		}
	}

	status, _ := classifyPipelineHealth(rawTotal, processedTotal, rawRecent, processedRecent, estimatedBacklog, &lagSeconds, windowMinutes, degradedBacklogThreshold, stalledBacklogThreshold, degradedLagThresholdS, stalledLagThresholdS)
	statusCode := int64(0)
	if status == "degraded" {
		statusCode = 1
	} else if status == "stalled" {
		statusCode = 2
	}

	var out strings.Builder
	out.WriteString("# HELP pyyol_lens_pipeline_status_code Pipeline health status (0=ok,1=degraded,2=stalled)\n")
	out.WriteString("# TYPE pyyol_lens_pipeline_status_code gauge\n")
	fmt.Fprintf(&out, "pyyol_lens_pipeline_status_code %d\n", statusCode)
	fmt.Fprintf(&out, "pyyol_lens_events_raw_total %d\n", rawTotal)
	fmt.Fprintf(&out, "pyyol_lens_processed_events_total %d\n", processedTotal)
	fmt.Fprintf(&out, "pyyol_lens_events_raw_recent %d\n", rawRecent)
	fmt.Fprintf(&out, "pyyol_lens_processed_events_recent %d\n", processedRecent)
	fmt.Fprintf(&out, "pyyol_lens_estimated_backlog_events %d\n", estimatedBacklog)
	fmt.Fprintf(&out, "pyyol_lens_estimated_lag_seconds %d\n", lagSeconds)
	fmt.Fprintf(&out, "pyyol_lens_traces_total %d\n", tracesTotal)
	fmt.Fprintf(&out, "pyyol_lens_spans_total %d\n", spansTotal)
	fmt.Fprintf(&out, "pyyol_lens_events_total %d\n", eventsTotal)
	fmt.Fprintf(&out, "pyyol_lens_token_usage_total %d\n", usageTotal)
	fmt.Fprintf(&out, "pyyol_lens_rollup_daily_total %d\n", rollupDailyTotal)
	fmt.Fprintf(&out, "pyyol_lens_health_window_minutes %d\n", windowMinutes)
	fmt.Fprintf(&out, "pyyol_lens_threshold_degraded_backlog_events %d\n", degradedBacklogThreshold)
	fmt.Fprintf(&out, "pyyol_lens_threshold_stalled_backlog_events %d\n", stalledBacklogThreshold)
	fmt.Fprintf(&out, "pyyol_lens_threshold_degraded_lag_seconds %d\n", degradedLagThresholdS)
	fmt.Fprintf(&out, "pyyol_lens_threshold_stalled_lag_seconds %d\n", stalledLagThresholdS)

	c.Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	return c.SendString(out.String())
}

func (h Handler) HealthReady(c *fiber.Ctx) error {
	check := func(q string) (int64, error) {
		row := h.Store.DB.QueryRowContext(c.UserContext(), q)
		var cnt int64
		if err := row.Scan(&cnt); err != nil {
			return 0, err
		}
		return cnt, nil
	}
	checkTime := func(q string) (*string, error) {
		row := h.Store.DB.QueryRowContext(c.UserContext(), q)
		var ts sql.NullTime
		if err := row.Scan(&ts); err != nil {
			return nil, err
		}
		if !ts.Valid {
			return nil, nil
		}
		iso := ts.Time.UTC().Format(time.RFC3339)
		return &iso, nil
	}

	windowMinutes := parseIntBounded(c.Query("window_minutes"), defaultHealthWindowMinutes, minHealthWindowMinutes, maxHealthWindowMinutes)
	degradedBacklogThreshold := parseInt64Min(c.Query("degraded_backlog"), defaultDegradedBacklogThreshold, 1)
	stalledBacklogThreshold := parseInt64Min(c.Query("stalled_backlog"), defaultStalledBacklogThreshold, degradedBacklogThreshold+1)
	degradedLagThresholdS := parseInt64Min(c.Query("degraded_lag_seconds"), defaultDegradedLagThresholdS, 1)
	stalledLagThresholdS := parseInt64Min(c.Query("stalled_lag_seconds"), defaultStalledLagThresholdS, degradedLagThresholdS+1)

	rawTotal, err := check(`SELECT count() FROM events_raw`)
	if err != nil {
		return c.Status(503).JSON(fiber.Map{"ready": false, "status": "error", "reason": "failed to read events_raw"})
	}
	processedTotal, err := check(`SELECT count() FROM processed_events`)
	if err != nil {
		return c.Status(503).JSON(fiber.Map{"ready": false, "status": "error", "reason": "failed to read processed_events"})
	}
	rawRecentQuery := fmt.Sprintf("SELECT count() FROM events_raw WHERE event_time >= now() - INTERVAL %d MINUTE", windowMinutes)
	processedRecentQuery := fmt.Sprintf("SELECT count() FROM processed_events WHERE processed_at >= now() - INTERVAL %d MINUTE", windowMinutes)
	rawRecent, err := check(rawRecentQuery)
	if err != nil {
		return c.Status(503).JSON(fiber.Map{"ready": false, "status": "error", "reason": "failed to read recent raw events"})
	}
	processedRecent, err := check(processedRecentQuery)
	if err != nil {
		return c.Status(503).JSON(fiber.Map{"ready": false, "status": "error", "reason": "failed to read recent processed events"})
	}

	rawLatest, err := checkTime(`SELECT max(event_time) FROM events_raw`)
	if err != nil {
		return c.Status(503).JSON(fiber.Map{"ready": false, "status": "error", "reason": "failed to read latest raw timestamp"})
	}
	processedLatest, err := checkTime(`SELECT max(processed_at) FROM processed_events`)
	if err != nil {
		return c.Status(503).JSON(fiber.Map{"ready": false, "status": "error", "reason": "failed to read latest processed timestamp"})
	}

	estimatedBacklog := rawTotal - processedTotal
	if estimatedBacklog < 0 {
		estimatedBacklog = 0
	}
	var lagSeconds int64
	if rawLatest != nil && processedLatest != nil {
		rawTs, parseRawErr := time.Parse(time.RFC3339, *rawLatest)
		processedTs, parseProcessedErr := time.Parse(time.RFC3339, *processedLatest)
		if parseRawErr == nil && parseProcessedErr == nil {
			seconds := int64(rawTs.Sub(processedTs).Seconds())
			if seconds > 0 {
				lagSeconds = seconds
			}
		}
	}

	status, reason := classifyPipelineHealth(rawTotal, processedTotal, rawRecent, processedRecent, estimatedBacklog, &lagSeconds, windowMinutes, degradedBacklogThreshold, stalledBacklogThreshold, degradedLagThresholdS, stalledLagThresholdS)
	httpStatus := fiber.StatusOK
	ready := true
	if status == "stalled" {
		httpStatus = fiber.StatusServiceUnavailable
		ready = false
	}

	return c.Status(httpStatus).JSON(fiber.Map{
		"ready":  ready,
		"status": status,
		"reason": reason,
		"counters": fiber.Map{
			"events_raw_total":        rawTotal,
			"processed_events_total":  processedTotal,
			"events_raw_recent":       rawRecent,
			"processed_events_recent": processedRecent,
			"estimated_backlog":       estimatedBacklog,
			"estimated_lag_seconds":   lagSeconds,
			"window_minutes":          windowMinutes,
		},
	})
}

const (
	defaultDegradedBacklogThreshold = int64(1000)
	defaultStalledBacklogThreshold  = int64(10000)
	defaultDegradedLagThresholdS    = int64(30)
	defaultStalledLagThresholdS     = int64(120)
	defaultHealthWindowMinutes      = 5
	minHealthWindowMinutes          = 1
	maxHealthWindowMinutes          = 60
)

func classifyPipelineHealth(rawTotal int64, processedTotal int64, rawRecent int64, processedRecent int64, estimatedBacklog int64, lagSeconds *int64, windowMinutes int, degradedBacklogThreshold int64, stalledBacklogThreshold int64, degradedLagThresholdS int64, stalledLagThresholdS int64) (string, string) {
	pipelineStatus := "ok"
	statusReason := "pipeline is healthy"
	if rawTotal > 0 && processedTotal == 0 {
		pipelineStatus = "stalled"
		statusReason = "raw events exist but no processed events yet"
	} else if rawRecent > 0 && processedRecent == 0 {
		pipelineStatus = "stalled"
		statusReason = fmt.Sprintf("ingest active but no events processed in last %d minutes", windowMinutes)
	} else if lagSeconds != nil && *lagSeconds >= stalledLagThresholdS {
		pipelineStatus = "stalled"
		statusReason = "processor lag is too high"
	} else if estimatedBacklog >= stalledBacklogThreshold {
		pipelineStatus = "stalled"
		statusReason = "processor backlog is too high"
	} else if (lagSeconds != nil && *lagSeconds >= degradedLagThresholdS) || estimatedBacklog >= degradedBacklogThreshold {
		pipelineStatus = "degraded"
		statusReason = "pipeline is processing but behind target latency/backlog"
	}
	return pipelineStatus, statusReason
}

func requireOrgID(c *fiber.Ctx) (string, error) {
	orgID := strings.TrimSpace(c.Get("x-organization-id"))
	if orgID == "" {
		return "", fiber.NewError(fiber.StatusBadRequest, "missing x-organization-id header")
	}
	return orgID, nil
}

func parseIntBounded(raw string, fallback int, min int, max int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func parseInt64Min(raw string, fallback int64, min int64) int64 {
	if raw == "" {
		if fallback < min {
			return min
		}
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < min {
		return min
	}
	return value
}

func parseInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
