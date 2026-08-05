-- 0072_model_benchmark_facts — the facts the model-benchmark board needs to be
-- honest about WHICH model played and WHAT it actually cost.
--
-- Everything added here is already MEASURED by the platform and was being thrown
-- away on the way to the database. Two groups:
--
-- 1. Model ATTRIBUTION per (match, agent), at three trust levels:
--      verified — the model string the upstream provider returned, read off the
--                 response by the Pyyol gateway. Server-observed, unfakeable.
--      observed — the model the SDK reported for the call it actually made
--                 (benchmark.TokenUsage.Model), per move.
--      declared — the manifest's `model:` block.
--    The board previously ranked on `declared` ALONE, which left it permanently
--    empty: `pyyol init`'s manifest template ships no model block, so no agent
--    scaffolded by the CLI could ever appear — while the gateway was reading the
--    real model name off every single response and discarding it.
--
--    Stored per-match, not per-agent, so a developer who switches models
--    mid-season has each match attributed to the model that actually played it.
--
-- 2. The token SPLIT and latency RANGE from the match.benchmark seat summary.
--    One `tokens` total cannot answer "what does this model burn on input vs
--    output", which is the question someone choosing a model actually has, and a
--    mean latency with no range hides a model that is usually fast and
--    occasionally times out.

BEGIN;

-- ── per-(match, agent) match facts ───────────────────────────────────────────
-- All NOT NULL DEFAULT 0/'' so historical rows stay readable: an old row reports
-- an unmeasured split as zero, and the read path renders unmeasured figures as a
-- dash rather than as a real zero.
ALTER TABLE agent_match_benchmark
    ADD COLUMN IF NOT EXISTS prompt_tokens     BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS completion_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS reasoning_tokens  BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cached_tokens     BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS latency_min_ms    BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS latency_max_ms    BIGINT NOT NULL DEFAULT 0,
    -- Failure taxonomy. `fallbacks` says the engine had to substitute a move; these
    -- say WHY, which is the difference between a model that reasons badly and an
    -- endpoint that is down.
    ADD COLUMN IF NOT EXISTS illegal           INT    NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS timeouts          INT    NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS transport_errors  INT    NOT NULL DEFAULT 0,
    -- Model attribution, best-effort at write time (see header).
    ADD COLUMN IF NOT EXISTS observed_provider TEXT   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS observed_model    TEXT   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS declared_provider TEXT   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS declared_model    TEXT   NOT NULL DEFAULT '';

-- ── gateway-verified economics ───────────────────────────────────────────────
-- The gateway already parses the provider's own response for usage and the model
-- name. Persisting them here makes the verified tier a complete economic record
-- rather than a bare cost, and makes `model` on this table the only unfakeable
-- model attribution the platform has.
ALTER TABLE agent_match_verified_cost
    ADD COLUMN IF NOT EXISTS provider          TEXT   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS model             TEXT   NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS prompt_tokens     BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS completion_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS total_tokens      BIGINT NOT NULL DEFAULT 0;

-- The board aggregates every benchmarked match in an arena for a season window,
-- so the scan is (game, match) and the join key back to a match is match_id.
CREATE INDEX IF NOT EXISTS idx_agent_match_benchmark_game_match
    ON agent_match_benchmark (game, match_id);

-- Season windows are resolved by matches.finished_at, which the aggregate joins
-- on public_id and then filters by. Without this the board's season filter is a
-- sequential scan of every match ever played.
CREATE INDEX IF NOT EXISTS idx_matches_finished_game
    ON matches (game, finished_at) WHERE finished_at IS NOT NULL;

COMMIT;
