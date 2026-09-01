-- Per-seat decision counts, precomputed.
--
-- Why this table exists
--
-- /v1/benchmark/harness/models and /v1/benchmark/developers computed verified-coverage by
-- aggregating agent_match_decisions on every request. With no filter the planner could not push
-- the season window through the aggregate, so each call did a GROUP BY over the whole decision
-- history — 10.2M rows and 23 GB on the lab database. Both endpoints ran until the caller's
-- context was cancelled and returned 500. Both are public.
--
-- Why not just read agent_match_benchmark.decisions
--
-- It is the same COUNT(*) over the same rows, so it looks like a free substitution. It is not:
-- sampled against the live table it is short by exactly one on about 4% of seats, because the
-- benchmark row is aggregated at finalize and a last decision can land after it. Swapping it in
-- would move a published coverage denominator by one decision on short seats — up to 14% on a
-- seven-decision seat — and nothing downstream would show that it had happened.
--
-- So: an exact rollup, refreshed incrementally, rather than a cheaper number that is quietly wrong.
CREATE TABLE IF NOT EXISTS agent_match_coverage (
    match_id          text        NOT NULL,
    agent_id          bigint      NOT NULL,
    -- Rows in agent_match_decisions for this seat. The coverage FRACTION's denominator.
    logged_decisions  bigint      NOT NULL DEFAULT 0,
    -- Distinct rounds in agent_match_bound_decisions. The numerator.
    bound_decisions   bigint      NOT NULL DEFAULT 0,
    computed_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (match_id, agent_id)
);

-- The refresh walks matches by finish time, so it needs to find its place cheaply.
CREATE INDEX IF NOT EXISTS idx_agent_match_coverage_computed
    ON agent_match_coverage (computed_at DESC);

-- A watermark, so a refresh is incremental rather than a full rebuild every tick. One row.
--
-- Stored rather than derived from max(computed_at) because a row is only written when a seat has
-- decisions: a window containing only empty matches would otherwise be re-scanned forever.
CREATE TABLE IF NOT EXISTS agent_match_coverage_watermark (
    id              int         PRIMARY KEY DEFAULT 1,
    covered_through timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_match_coverage_watermark_single CHECK (id = 1)
);
