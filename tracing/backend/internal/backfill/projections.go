package backfill

import (
	"context"
	"fmt"

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

// rollupTables are SummingMergeTree, so re-inserting an aggregate ADDS to it rather than
// replacing it. Every other projection here is keyed and collapses on merge, which makes it
// idempotent; these three are not.
var rollupTables = []string{"rollup_hourly", "rollup_daily", "rollup_monthly"}

// truncateRollups clears the rollup tables before they are re-derived.
//
// ALWAYS, not only under BACKFILL_RESET. Re-deriving a rollup from events_raw is by definition a
// full replacement, and because these tables SUM on merge, a run without reset silently DOUBLES
// them: measured 381,060 tokens against a true 190,530 after one such run. Nothing errored and
// nothing looked wrong — the numbers were simply twice reality, which on a cost board is worse
// than a crash.
//
// The reset flag also could not be relied on to prevent this: it compares the env var against the
// literal "true", so the obvious BACKFILL_RESET=1 reads as false and the caller gets the unsafe
// path while believing they asked for the safe one.
func truncateRollups(ctx context.Context, ch *store.Store) error {
	for _, tbl := range rollupTables {
		if _, err := ch.DB.ExecContext(ctx, "TRUNCATE TABLE "+tbl); err != nil {
			return fmt.Errorf("truncate %s before re-deriving rollups: %w", tbl, err)
		}
	}
	return nil
}

// InsertProjectionsFromEventsRaw rebuilds traces, spans, events, token_usage, rollups, etc. from events_raw.
//
// # QUIESCE THE PIPELINE FIRST
//
// The rollups are re-derived by truncating and re-selecting, which is correct only if nothing
// is writing them meanwhile. The live processor writes a rollup delta per event as it arrives,
// so an event landing between the TRUNCATE and the SELECT is counted twice — once by its own
// delta and once by the re-derivation.
//
// Measured: a backfill run while lab matches were in flight left the rollups 5,220 gateway
// tokens and 6,643 sdk tokens ABOVE events_raw. Nothing errored. With traffic stopped and the
// consumer drained, the same run landed on ground truth exactly, both meters.
//
// So: stop the producers, wait for the NATS consumer to reach zero pending, then run this.
//
//	docker exec <nats> sh -c "wget -qO- 'http://127.0.0.1:8222/jsz?consumers=true&streams=true'"
//	# every consumer's num_pending must be 0 before starting
//
// Fixing the race properly means deriving into a shadow table and swapping, or excluding events
// newer than a watermark. Neither is worth building until a backfill has to run on a live
// system; today it is a repair tool, and the requirement is written down rather than assumed.
func InsertProjectionsFromEventsRaw(ctx context.Context, ch *store.Store) error {
	// The rollups must start empty every time; see truncateRollups.
	if err := truncateRollups(ctx, ch); err != nil {
		return err
	}

	queries := []string{
		`INSERT INTO traces (
			trace_id, request_id, organization_id, project_id, environment, user_id, actor_id,
			session_id, started_at, ended_at, status, root_input_ref, root_output_ref, total_tokens,
			total_cost, latency_ms, error_type, error_message, workflow_version,
			prompt_version_ids_json, model_config_versions_json, sampling_reason,
			redaction_summary_json, updated_at
		)
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

		// EXPLICIT column list, not a positional insert.
		//
		// This selected 21 expressions into `spans` with no column list, which worked until the
		// phase-10 telemetry migration widened the table to 38 columns. After that the whole
		// backfill died on "Number of columns doesn't match (source: 21 and result: 38)" — and
		// because the queries run in sequence, ONE broken projection meant no projection could be
		// rebuilt at all.
		//
		// Naming the columns is what stops that recurring: the 17 later columns take their
		// defaults, and a 39th added tomorrow cannot break this again. The alternative — appending
		// 17 placeholder expressions — would have to be edited on every future migration, which is
		// the same trap one release further out.
		`INSERT INTO spans (
			trace_id, span_id, parent_span_id, span_type, step_name, status,
			started_at, ended_at, latency_ms, input_ref, output_ref,
			error_type, error_message, provider, model, model_version,
			tool_name, tool_version, total_tokens, total_cost, updated_at
		)
		SELECT
			trace_id,
			span_id,
			anyLast(parent_span_id),
			anyLast(span_type),
			anyLast(step_name),
			anyLast(status),
			min(event_time),
			max(event_time),
			-- A LEAF span has one event, so the timestamp span is zero and the real duration is
			-- the one the producer measured. Deriving it from min/max alone reported every
			-- gateway model call as 0ms — a latency column that is always zero is worse than an
			-- absent one, because a reader takes it for a measurement.
			greatest(dateDiff('millisecond', min(event_time), max(event_time)), max(latency_ms)),
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

		`INSERT INTO events (
			trace_id, span_id, event_id, event_type, event_time, status, source_service,
			payload_ref, error_type, error_message
		)
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

		`INSERT INTO token_usage (
			trace_id, span_id, project_id, environment, provider, model, model_version,
			prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens, total_tokens,
			estimated_cost, reconciled_cost, currency, pricing_version, meter_source, event_time
		)
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

		`INSERT INTO tool_calls (
			trace_id, span_id, tool_name, tool_version, status, event_time, error_message,
			payload_ref, total_tokens, estimated_cost
		)
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

		`INSERT INTO retrievals (
			trace_id, span_id, step_name, status, event_time, payload_ref, error_message
		)
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

		`INSERT INTO match_events (
			run_id, trace_id, event_type, event_time, status, error_message
		)
		SELECT
			run_id,
			trace_id,
			event_type,
			event_time,
			status,
			error_message
		FROM events_raw
		WHERE run_id != ''`,

		`INSERT INTO processed_events (
			event_id, trace_id, ingestion_id, processed_at
		)
		SELECT
			event_id,
			trace_id,
			ingestion_id,
			now64(3) AS processed_at
		FROM events_raw`,

		// meter_source is part of the GROUP BY, not just the projection. A gateway-routed call
		// emits one gateway event and one sdk event, so grouping without it sums a measurement
		// and a claim into a single number and counts every such call twice. See migration 006.
		`INSERT INTO rollup_hourly (
			bucket_start, organization_id, project_id, environment, traces_total, errors_total,
			tokens_total, estimated_cost, meter_source
		)
		SELECT
			toStartOfHour(event_time) AS bucket_start,
			organization_id,
			project_id,
			environment,
			countIf(event_type = 'trace_started') AS traces_total,
			countIf(event_type = 'trace_failed' OR status = 'error') AS errors_total,
			sum(total_tokens) AS tokens_total,
			sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)) AS estimated_cost,
			meter_source
		FROM events_raw
		GROUP BY bucket_start, organization_id, project_id, environment, meter_source`,

		// meter_source is part of the GROUP BY, not just the projection. A gateway-routed call
		// emits one gateway event and one sdk event, so grouping without it sums a measurement
		// and a claim into a single number and counts every such call twice. See migration 006.
		`INSERT INTO rollup_daily (
			bucket_start, organization_id, project_id, environment, traces_total, errors_total,
			tokens_total, estimated_cost, meter_source
		)
		SELECT
			toDate(event_time) AS bucket_start,
			organization_id,
			project_id,
			environment,
			countIf(event_type = 'trace_started') AS traces_total,
			countIf(event_type = 'trace_failed' OR status = 'error') AS errors_total,
			sum(total_tokens) AS tokens_total,
			sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)) AS estimated_cost,
			meter_source
		FROM events_raw
		GROUP BY bucket_start, organization_id, project_id, environment, meter_source`,

		// meter_source is part of the GROUP BY, not just the projection. A gateway-routed call
		// emits one gateway event and one sdk event, so grouping without it sums a measurement
		// and a claim into a single number and counts every such call twice. See migration 006.
		`INSERT INTO rollup_monthly (
			bucket_start, organization_id, project_id, environment, traces_total, errors_total,
			tokens_total, estimated_cost, meter_source
		)
		SELECT
			toDate(toStartOfMonth(event_time)) AS bucket_start,
			organization_id,
			project_id,
			environment,
			countIf(event_type = 'trace_started') AS traces_total,
			countIf(event_type = 'trace_failed' OR status = 'error') AS errors_total,
			sum(total_tokens) AS tokens_total,
			sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)) AS estimated_cost,
			meter_source
		FROM events_raw
		GROUP BY bucket_start, organization_id, project_id, environment, meter_source`,
	}

	for _, q := range queries {
		if _, err := ch.DB.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
