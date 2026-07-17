CREATE DATABASE IF NOT EXISTS pyyol_lens;

CREATE TABLE IF NOT EXISTS pyyol_lens.events_raw
(
  ingestion_id String,
  event_id String,
  trace_id String,
  request_id String,
  span_id String,
  parent_span_id String,
  event_type String,
  event_time DateTime64(3),
  ingested_at DateTime64(3),
  sequence_number Int64,
  source_service String,
  schema_version String,
  status String,
  organization_id String,
  project_id String,
  environment String,
  user_id String,
  actor_id String,
  session_id String,
  run_id String,
  conversation_id String,
  app_id String,
  queue_job_id String,
  component String,
  operation String,
  span_type String,
  step_name String,
  provider String,
  model String,
  model_version String,
  tool_name String,
  tool_version String,
  root_input_ref String,
  root_output_ref String,
  payload_ref String,
  payload_json String,
  prompt_version_ids_json String,
  model_config_versions_json String,
  latency_ms Int64,
  input_bytes Int64,
  output_bytes Int64,
  prompt_tokens Int64,
  completion_tokens Int64,
  cached_tokens Int64,
  reasoning_tokens Int64,
  total_tokens Int64,
  estimated_cost Float64,
  reconciled_cost Float64,
  currency String,
  pricing_version String,
  meter_source String,
  error_type String,
  error_code String,
  error_message String,
  sampling_reason String,
  redaction_summary_json String,
  task_kind String,
  archetype String,
  scope String,
  subagent_id String,
  parent_subagent_id String,
  artifact_ids_in_json String,
  artifact_ids_out_json String,
  evidence_ids_out_json String,
  citation_ids_out_json String,
  reducer_name String,
  reduction_ratio Float64,
  tool_token_savings Int64,
  budget_iterations_used Int64,
  budget_iterations_cap Int64,
  budget_tokens_used Int64,
  budget_tokens_cap Int64
)
ENGINE = MergeTree
PARTITION BY toDate(event_time)
ORDER BY (organization_id, project_id, environment, trace_id, event_time, event_id)
TTL toDateTime(event_time) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS pyyol_lens.traces
(
  trace_id String,
  request_id String,
  organization_id String,
  project_id String,
  environment String,
  user_id String,
  actor_id String,
  session_id String,
  started_at DateTime64(3),
  ended_at DateTime64(3),
  status String,
  root_input_ref String,
  root_output_ref String,
  total_tokens Int64,
  total_cost Float64,
  latency_ms Int64,
  error_type String,
  error_message String,
  workflow_version String,
  prompt_version_ids_json String,
  model_config_versions_json String,
  sampling_reason String,
  redaction_summary_json String,
  updated_at DateTime64(3)
)
ENGINE = ReplacingMergeTree(updated_at)
PARTITION BY toDate(started_at)
ORDER BY (organization_id, project_id, environment, started_at, trace_id);

CREATE TABLE IF NOT EXISTS pyyol_lens.spans
(
  trace_id String,
  span_id String,
  parent_span_id String,
  span_type String,
  step_name String,
  status String,
  started_at DateTime64(3),
  ended_at DateTime64(3),
  latency_ms Int64,
  input_ref String,
  output_ref String,
  error_type String,
  error_message String,
  provider String,
  model String,
  model_version String,
  tool_name String,
  tool_version String,
  total_tokens Int64,
  total_cost Float64,
  updated_at DateTime64(3),
  task_kind String,
  archetype String,
  scope String,
  subagent_id String,
  parent_subagent_id String,
  artifact_ids_in_json String,
  artifact_ids_out_json String,
  evidence_ids_out_json String,
  citation_ids_out_json String,
  reducer_name String,
  reduction_ratio Float64,
  tool_token_savings Int64,
  budget_iterations_used Int64,
  budget_iterations_cap Int64,
  budget_tokens_used Int64,
  budget_tokens_cap Int64,
  last_event_type String
)
ENGINE = ReplacingMergeTree(updated_at)
PARTITION BY toDate(started_at)
ORDER BY (trace_id, started_at, span_id);

CREATE TABLE IF NOT EXISTS pyyol_lens.events
(
  trace_id String,
  span_id String,
  event_id String,
  event_type String,
  event_time DateTime64(3),
  status String,
  source_service String,
  payload_ref String,
  error_type String,
  error_message String
)
ENGINE = MergeTree
PARTITION BY toDate(event_time)
ORDER BY (trace_id, event_time, event_id);

CREATE TABLE IF NOT EXISTS pyyol_lens.token_usage
(
  trace_id String,
  span_id String,
  project_id String,
  environment String,
  provider String,
  model String,
  model_version String,
  prompt_tokens Int64,
  completion_tokens Int64,
  cached_tokens Int64,
  reasoning_tokens Int64,
  total_tokens Int64,
  estimated_cost Float64,
  reconciled_cost Float64,
  currency String,
  pricing_version String,
  meter_source String,
  event_time DateTime64(3)
)
ENGINE = MergeTree
PARTITION BY toDate(event_time)
ORDER BY (project_id, environment, provider, model, event_time, trace_id);

CREATE TABLE IF NOT EXISTS pyyol_lens.tool_calls
(
  trace_id String,
  span_id String,
  tool_name String,
  tool_version String,
  status String,
  event_time DateTime64(3),
  error_message String,
  payload_ref String,
  total_tokens Int64,
  estimated_cost Float64
)
ENGINE = MergeTree
PARTITION BY toDate(event_time)
ORDER BY (tool_name, event_time, trace_id, span_id);

CREATE TABLE IF NOT EXISTS pyyol_lens.retrievals
(
  trace_id String,
  span_id String,
  step_name String,
  status String,
  event_time DateTime64(3),
  payload_ref String,
  error_message String
)
ENGINE = MergeTree
PARTITION BY toDate(event_time)
ORDER BY (event_time, trace_id, span_id);

-- Per-match denormalized event stream (keyed by run_id = match id) for the
-- "all events for match X" view. Populated for any event carrying a run_id.
CREATE TABLE IF NOT EXISTS pyyol_lens.match_events
(
  run_id String,
  trace_id String,
  event_type String,
  event_time DateTime64(3),
  status String,
  error_message String
)
ENGINE = MergeTree
PARTITION BY toDate(event_time)
ORDER BY (run_id, event_time, trace_id);

CREATE TABLE IF NOT EXISTS pyyol_lens.evaluations
(
  run_id String,
  dataset_id String,
  dataset_version String,
  prompt_version String,
  model_config_version String,
  workflow_version String,
  score_summary_json String,
  failures_json String,
  comments String,
  comparison_baseline String,
  created_at DateTime64(3)
)
ENGINE = MergeTree
PARTITION BY toDate(created_at)
ORDER BY (created_at, run_id);

CREATE TABLE IF NOT EXISTS pyyol_lens.replays
(
  replay_id String,
  trace_id String,
  mode String,
  prompt_version String,
  model_config_version String,
  retrieval_config String,
  tool_config String,
  environment String,
  status String,
  outcome_ref String,
  diff_ref String,
  created_at DateTime64(3),
  run_id String,
  replay_of String,
  derived_from_json String,
  reducer_name String,
  request_json String,
  organization_id String
)
ENGINE = MergeTree
PARTITION BY toDate(created_at)
ORDER BY (created_at, replay_id);

CREATE TABLE IF NOT EXISTS pyyol_lens.rollup_hourly
(
  bucket_start DateTime,
  organization_id String,
  project_id String,
  environment String,
  traces_total Int64,
  errors_total Int64,
  tokens_total Int64,
  estimated_cost Float64
)
ENGINE = SummingMergeTree
PARTITION BY toDate(bucket_start)
ORDER BY (organization_id, project_id, environment, bucket_start);

CREATE TABLE IF NOT EXISTS pyyol_lens.rollup_daily
(
  bucket_start Date,
  organization_id String,
  project_id String,
  environment String,
  traces_total Int64,
  errors_total Int64,
  tokens_total Int64,
  estimated_cost Float64
)
ENGINE = SummingMergeTree
PARTITION BY bucket_start
ORDER BY (organization_id, project_id, environment, bucket_start);

CREATE TABLE IF NOT EXISTS pyyol_lens.rollup_monthly
(
  bucket_start Date,
  organization_id String,
  project_id String,
  environment String,
  traces_total Int64,
  errors_total Int64,
  tokens_total Int64,
  estimated_cost Float64
)
ENGINE = SummingMergeTree
PARTITION BY bucket_start
ORDER BY (organization_id, project_id, environment, bucket_start);

CREATE TABLE IF NOT EXISTS pyyol_lens.processed_events
(
  event_id String,
  trace_id String,
  ingestion_id String,
  processed_at DateTime64(3)
)
ENGINE = ReplacingMergeTree(processed_at)
ORDER BY (event_id);
