-- Retention. Only `events_raw` had a TTL (30 days); every other table grew forever.
--
-- Two problems, not one:
--
-- 1. STORAGE. `processed_events` is the ingest dedupe table — one row for every event
--    ever accepted, with no partitioning and no expiry. It is the first table that
--    hurts, and it hurts silently.
--
-- 2. CORRECTNESS. The 30-day TTL on `events_raw` is where per-decision detail,
--    rationale and per-move tokens live, while the derived leaderboard rows persist
--    forever. So a match older than 30 days still counts toward an agent's standing
--    but can no longer be audited — the evidence expires before the verdict does.
--    The fix for that is to keep the DERIVED tables materially longer than the raw
--    firehose, which is what the windows below do.
--
-- Windows are deliberately tiered by how expensive the row is and how long it stays
-- useful: the raw firehose is short, per-match structure is medium, and the rollups
-- that power leaderboards are long.
--
-- ClickHouse applies TTL during background merges, so expiry is eventual, not exact.
-- That is fine here: nothing depends on a row disappearing at a precise instant.

-- Per-match structure: enough to reconstruct a match long after it settled, without
-- retaining the full raw payloads that back it.
ALTER TABLE pyyol_lens.traces       MODIFY TTL toDateTime(started_at)  + INTERVAL 180 DAY;
ALTER TABLE pyyol_lens.spans        MODIFY TTL toDateTime(started_at)  + INTERVAL 180 DAY;
ALTER TABLE pyyol_lens.events       MODIFY TTL toDateTime(event_time)  + INTERVAL 180 DAY;
ALTER TABLE pyyol_lens.match_events MODIFY TTL toDateTime(event_time)  + INTERVAL 180 DAY;

-- Cost/usage detail: the basis of billing and cost disputes, so kept a full year.
ALTER TABLE pyyol_lens.token_usage  MODIFY TTL toDateTime(event_time)  + INTERVAL 365 DAY;

-- Supporting detail: useful for debugging a specific run, not for long-run analysis.
ALTER TABLE pyyol_lens.tool_calls   MODIFY TTL toDateTime(event_time)  + INTERVAL 90 DAY;
ALTER TABLE pyyol_lens.retrievals   MODIFY TTL toDateTime(event_time)  + INTERVAL 90 DAY;

-- Dedupe bookkeeping. This only needs to outlive the window in which a duplicate
-- could plausibly be re-delivered (retries, replays, an operator re-running a batch),
-- NOT the lifetime of the data itself. 30 days is far beyond any retry horizon.
ALTER TABLE pyyol_lens.processed_events MODIFY TTL toDateTime(processed_at) + INTERVAL 30 DAY;

-- Rollups are small and are what leaderboards read, so they outlive everything else.
-- No TTL on rollup_monthly at all: it is the long-term record.
ALTER TABLE pyyol_lens.rollup_hourly    MODIFY TTL toDateTime(bucket_start) + INTERVAL 90 DAY;
ALTER TABLE pyyol_lens.rollup_daily     MODIFY TTL toDateTime(bucket_start) + INTERVAL 730 DAY;

-- Data-skipping indexes for the two filters every agent-facing query uses.
-- These were written in 002_phase10_telemetry.sql and left COMMENTED OUT, so every
-- lookup by match or event type was a full partition scan on the largest table.
ALTER TABLE pyyol_lens.events_raw ADD INDEX IF NOT EXISTS idx_run_id     run_id     TYPE bloom_filter(0.01) GRANULARITY 4;
ALTER TABLE pyyol_lens.events_raw ADD INDEX IF NOT EXISTS idx_event_type event_type TYPE set(0)             GRANULARITY 4;
ALTER TABLE pyyol_lens.events_raw ADD INDEX IF NOT EXISTS idx_actor_id   actor_id   TYPE bloom_filter(0.01) GRANULARITY 4;
