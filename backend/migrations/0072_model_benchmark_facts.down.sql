BEGIN;

DROP INDEX IF EXISTS idx_matches_finished_game;
DROP INDEX IF EXISTS idx_agent_match_benchmark_game_match;

ALTER TABLE agent_match_verified_cost
    DROP COLUMN IF EXISTS provider,
    DROP COLUMN IF EXISTS model,
    DROP COLUMN IF EXISTS prompt_tokens,
    DROP COLUMN IF EXISTS completion_tokens,
    DROP COLUMN IF EXISTS total_tokens;

ALTER TABLE agent_match_benchmark
    DROP COLUMN IF EXISTS prompt_tokens,
    DROP COLUMN IF EXISTS completion_tokens,
    DROP COLUMN IF EXISTS reasoning_tokens,
    DROP COLUMN IF EXISTS cached_tokens,
    DROP COLUMN IF EXISTS latency_min_ms,
    DROP COLUMN IF EXISTS latency_max_ms,
    DROP COLUMN IF EXISTS illegal,
    DROP COLUMN IF EXISTS timeouts,
    DROP COLUMN IF EXISTS transport_errors,
    DROP COLUMN IF EXISTS observed_provider,
    DROP COLUMN IF EXISTS observed_model,
    DROP COLUMN IF EXISTS declared_provider,
    DROP COLUMN IF EXISTS declared_model;

COMMIT;
