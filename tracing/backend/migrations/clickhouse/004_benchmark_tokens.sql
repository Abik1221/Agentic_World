-- Token economics on the agent benchmark rollup. The arena's benchmark_recorded
-- events now carry per-seat token aggregates in payload_json (prompt/completion/
-- reasoning/total); previously the projection dropped them. Add summed columns so
-- token usage rolls up per (agent, game, mode, day) alongside the decision-quality
-- counters. SummingMergeTree sums any numeric column outside the ORDER BY key, so
-- these are additive with no engine change. Backfill of existing rows is a replay
-- of retained benchmark_recorded events (payload_json still holds the values).
ALTER TABLE pyyol_lens.agent_benchmarks ADD COLUMN IF NOT EXISTS prompt_tokens     Int64 DEFAULT 0;
ALTER TABLE pyyol_lens.agent_benchmarks ADD COLUMN IF NOT EXISTS completion_tokens Int64 DEFAULT 0;
ALTER TABLE pyyol_lens.agent_benchmarks ADD COLUMN IF NOT EXISTS reasoning_tokens  Int64 DEFAULT 0;
ALTER TABLE pyyol_lens.agent_benchmarks ADD COLUMN IF NOT EXISTS total_tokens      Int64 DEFAULT 0;
