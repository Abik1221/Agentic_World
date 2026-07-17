package backfill

import (
	"context"

	"github.com/agent-arena/pyyol-lens/backend/internal/store"
)

// ResetProjectionTables truncates derived tables so they can be rebuilt from events_raw.
func ResetProjectionTables(ctx context.Context, ch *store.Store) error {
	tables := []string{
		"processed_events",
		"match_events",
		"retrievals",
		"tool_calls",
		"token_usage",
		"events",
		"spans",
		"traces",
		"rollup_hourly",
		"rollup_daily",
		"rollup_monthly",
	}
	for _, tbl := range tables {
		if _, err := ch.DB.ExecContext(ctx, "TRUNCATE TABLE "+tbl); err != nil {
			return err
		}
	}
	return nil
}

// InsertProjectionsFromEventsRaw rebuilds traces, spans, events, token_usage, rollups, etc. from events_raw.
func InsertProjectionsFromEventsRaw(ctx context.Context, ch *store.Store) error {
	queries := []string{
		`INSERT INTO traces
		SELECT
			trace_id,
			anyLast(request_id),
			anyLast(organization_id),
			anyLast(project_id),
			anyLast(environment),
			anyLast(user_id),
			anyLast(actor_id),
			anyLast(session_id),
			min(event_time) AS started_at,
			max(event_time) AS ended_at,
			anyLast(status),
			anyLast(root_input_ref),
			anyLast(root_output_ref),
			sum(total_tokens),
			sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)),
			dateDiff('millisecond', min(event_time), max(event_time)),
			anyLast(error_type),
			anyLast(error_message),
			'' AS workflow_version,
			anyLast(prompt_version_ids_json),
			anyLast(model_config_versions_json),
			anyLast(sampling_reason),
			anyLast(redaction_summary_json),
			now64(3) AS updated_at
		FROM events_raw
		WHERE trace_id != ''
		GROUP BY trace_id`,

		`INSERT INTO spans
		SELECT
			trace_id,
			span_id,
			anyLast(parent_span_id),
			anyLast(span_type),
			anyLast(step_name),
			anyLast(status),
			min(event_time),
			max(event_time),
			dateDiff('millisecond', min(event_time), max(event_time)),
			anyLast(root_input_ref),
			anyLast(root_output_ref),
			anyLast(error_type),
			anyLast(error_message),
			anyLast(provider),
			anyLast(model),
			anyLast(model_version),
			anyLast(tool_name),
			anyLast(tool_version),
			sum(total_tokens),
			sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)),
			now64(3) AS updated_at
		FROM events_raw
		WHERE trace_id != '' AND span_id != ''
		GROUP BY trace_id, span_id`,

		`INSERT INTO events
		SELECT
			trace_id,
			span_id,
			event_id,
			event_type,
			event_time,
			status,
			source_service,
			payload_ref,
			error_type,
			error_message
		FROM events_raw`,

		`INSERT INTO token_usage
		SELECT
			trace_id,
			span_id,
			project_id,
			environment,
			provider,
			model,
			model_version,
			prompt_tokens,
			completion_tokens,
			cached_tokens,
			reasoning_tokens,
			total_tokens,
			estimated_cost,
			reconciled_cost,
			currency,
			pricing_version,
			meter_source,
			event_time
		FROM events_raw
		WHERE total_tokens > 0 OR estimated_cost > 0 OR reconciled_cost > 0`,

		`INSERT INTO tool_calls
		SELECT
			trace_id,
			span_id,
			tool_name,
			tool_version,
			status,
			event_time,
			error_message,
			payload_ref,
			total_tokens,
			estimated_cost
		FROM events_raw
		WHERE tool_name != ''`,

		`INSERT INTO retrievals
		SELECT
			trace_id,
			span_id,
			step_name,
			status,
			event_time,
			payload_ref,
			error_message
		FROM events_raw
		WHERE event_type IN ('retrieval_started','retrieval_completed','rerank_started','rerank_completed')`,

		`INSERT INTO match_events
		SELECT
			run_id,
			trace_id,
			event_type,
			event_time,
			status,
			error_message
		FROM events_raw
		WHERE run_id != ''`,

		`INSERT INTO processed_events
		SELECT
			event_id,
			trace_id,
			ingestion_id,
			now64(3) AS processed_at
		FROM events_raw`,

		`INSERT INTO rollup_hourly
		SELECT
			toStartOfHour(event_time) AS bucket_start,
			organization_id,
			project_id,
			environment,
			countIf(event_type = 'trace_started') AS traces_total,
			countIf(event_type = 'trace_failed' OR status = 'error') AS errors_total,
			sum(total_tokens) AS tokens_total,
			sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)) AS estimated_cost
		FROM events_raw
		GROUP BY bucket_start, organization_id, project_id, environment`,

		`INSERT INTO rollup_daily
		SELECT
			toDate(event_time) AS bucket_start,
			organization_id,
			project_id,
			environment,
			countIf(event_type = 'trace_started') AS traces_total,
			countIf(event_type = 'trace_failed' OR status = 'error') AS errors_total,
			sum(total_tokens) AS tokens_total,
			sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)) AS estimated_cost
		FROM events_raw
		GROUP BY bucket_start, organization_id, project_id, environment`,

		`INSERT INTO rollup_monthly
		SELECT
			toDate(toStartOfMonth(event_time)) AS bucket_start,
			organization_id,
			project_id,
			environment,
			countIf(event_type = 'trace_started') AS traces_total,
			countIf(event_type = 'trace_failed' OR status = 'error') AS errors_total,
			sum(total_tokens) AS tokens_total,
			sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)) AS estimated_cost
		FROM events_raw
		GROUP BY bucket_start, organization_id, project_id, environment`,
	}

	for _, q := range queries {
		if _, err := ch.DB.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
