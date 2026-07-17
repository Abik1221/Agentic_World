-- Phase 10: additive telemetry fields on events_raw and spans, replay provenance.
-- Run once after 001_init.sql. Requires ClickHouse 21+ (ADD COLUMN IF NOT EXISTS).
--
-- Optional data-skipping indexes (run manually if your CH version supports the syntax):
--   ALTER TABLE pyyol_lens.events_raw ADD INDEX idx_run_id run_id TYPE bloom_filter(0.01) GRANULARITY 1;
--   ALTER TABLE pyyol_lens.events_raw ADD INDEX idx_event_type event_type TYPE bloom_filter(0.01) GRANULARITY 1;
--   ALTER TABLE pyyol_lens.events_raw ADD INDEX idx_org_task_time (organization_id, task_kind, event_time) TYPE minmax GRANULARITY 1;
--   ALTER TABLE pyyol_lens.events_raw ADD INDEX idx_org_archetype_time (organization_id, archetype, event_time) TYPE minmax GRANULARITY 1;

ALTER TABLE pyyol_lens.events_raw
  ADD COLUMN IF NOT EXISTS task_kind String,
  ADD COLUMN IF NOT EXISTS archetype String,
  ADD COLUMN IF NOT EXISTS scope String,
  ADD COLUMN IF NOT EXISTS subagent_id String,
  ADD COLUMN IF NOT EXISTS parent_subagent_id String,
  ADD COLUMN IF NOT EXISTS artifact_ids_in_json String,
  ADD COLUMN IF NOT EXISTS artifact_ids_out_json String,
  ADD COLUMN IF NOT EXISTS evidence_ids_out_json String,
  ADD COLUMN IF NOT EXISTS citation_ids_out_json String,
  ADD COLUMN IF NOT EXISTS reducer_name String,
  ADD COLUMN IF NOT EXISTS reduction_ratio Float64,
  ADD COLUMN IF NOT EXISTS tool_token_savings Int64,
  ADD COLUMN IF NOT EXISTS budget_iterations_used Int64,
  ADD COLUMN IF NOT EXISTS budget_iterations_cap Int64,
  ADD COLUMN IF NOT EXISTS budget_tokens_used Int64,
  ADD COLUMN IF NOT EXISTS budget_tokens_cap Int64;

ALTER TABLE pyyol_lens.spans
  ADD COLUMN IF NOT EXISTS task_kind String,
  ADD COLUMN IF NOT EXISTS archetype String,
  ADD COLUMN IF NOT EXISTS scope String,
  ADD COLUMN IF NOT EXISTS subagent_id String,
  ADD COLUMN IF NOT EXISTS parent_subagent_id String,
  ADD COLUMN IF NOT EXISTS artifact_ids_in_json String,
  ADD COLUMN IF NOT EXISTS artifact_ids_out_json String,
  ADD COLUMN IF NOT EXISTS evidence_ids_out_json String,
  ADD COLUMN IF NOT EXISTS citation_ids_out_json String,
  ADD COLUMN IF NOT EXISTS reducer_name String,
  ADD COLUMN IF NOT EXISTS reduction_ratio Float64,
  ADD COLUMN IF NOT EXISTS tool_token_savings Int64,
  ADD COLUMN IF NOT EXISTS budget_iterations_used Int64,
  ADD COLUMN IF NOT EXISTS budget_iterations_cap Int64,
  ADD COLUMN IF NOT EXISTS budget_tokens_used Int64,
  ADD COLUMN IF NOT EXISTS budget_tokens_cap Int64,
  ADD COLUMN IF NOT EXISTS last_event_type String;

ALTER TABLE pyyol_lens.replays
  ADD COLUMN IF NOT EXISTS run_id String,
  ADD COLUMN IF NOT EXISTS replay_of String,
  ADD COLUMN IF NOT EXISTS derived_from_json String,
  ADD COLUMN IF NOT EXISTS reducer_name String,
  ADD COLUMN IF NOT EXISTS request_json String,
  ADD COLUMN IF NOT EXISTS organization_id String;
