-- Agent benchmark rollup: one summed row per (agent, game, mode, day) fed from
-- benchmark_recorded events. SummingMergeTree collapses same-key rows so the
-- leaderboard is a cheap GROUP BY. Rates + avg latency are derived at query time
-- (legal/decisions, fallbacks/decisions, latency_sum_ms/decisions).
CREATE TABLE IF NOT EXISTS pyyol_lens.agent_benchmarks
(
  bucket_start Date,
  organization_id String,
  project_id String,
  environment String,
  game String,
  mode String,
  agent_id String,
  agent_version String,
  provider String,
  model String,
  matches Int64,
  decisions Int64,
  legal Int64,
  illegal Int64,
  timeouts Int64,
  transport_errors Int64,
  disconnects Int64,
  errors Int64,
  fallbacks Int64,
  latency_sum_ms Int64,
  wins Int64,
  losses Int64,
  draws Int64
)
ENGINE = SummingMergeTree
PARTITION BY bucket_start
ORDER BY (organization_id, project_id, environment, game, mode, agent_id, agent_version, provider, model, bucket_start);
