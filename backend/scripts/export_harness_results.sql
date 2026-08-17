-- Export the PLATFORM HARNESS benchmark result set as one portable JSON document.
--
-- Why this exists. The benchmark is real — real models, real provider calls, real outcomes —
-- but it was run against a lab database, and a benchmark nobody can read has not been
-- published. Re-running it against production is the cleaner path and remains the right one
-- for future runs; this moves the results already paid for.
--
-- PORTABILITY IS THE WHOLE DESIGN. Every foreign key here is exported as a PUBLIC id, never
-- an internal integer, because `agents.id = 1797` means a different agent in every database.
-- The importer re-resolves them on the far side. An export that carried internal ids would
-- import cleanly and attach every row to the wrong agent.
--
-- WHAT IS DELIBERATELY NOT EXPORTED: the owning users. The lab created one throwaway account
-- per seat (`lab+…@pyyol.test`); importing those would put fake developers into production,
-- which is the exact thing the platform-owned agent work removed. On import every agent is
-- re-owned by `usr_system`.
\set ON_ERROR_STOP on
\t on
\a
\pset format unaligned

WITH h_agents AS (
  SELECT DISTINCT a.id, a.public_id, a.name, a.slug, a.description, a.kind
    FROM agents a WHERE a.kind = 'harness'
),
h_matches AS (
  SELECT DISTINCT m.id, m.public_id, m.game, m.rated, m.started_at, m.finished_at, m.status,
         m.engine_version, m.prize_seed_commit, m.total_rounds
    FROM matches m
    JOIN agent_match_benchmark b ON b.match_id = m.public_id
    JOIN h_agents a ON a.id = b.agent_id
   WHERE m.finished_at IS NOT NULL
)
SELECT json_build_object(
  'exported_at', now(),
  -- Stamped so an importer can say WHICH run it is loading, and so loading the same export
  -- twice is recognisable rather than merely idempotent.
  'source', 'pyyol-lab',
  'agents', (SELECT coalesce(json_agg(json_build_object(
        'public_id', a.public_id, 'name', a.name, 'slug', a.slug,
        'description', a.description, 'kind', a.kind)), '[]'::json) FROM h_agents a),
  'matches', (SELECT coalesce(json_agg(json_build_object(
        'public_id', m.public_id, 'game', m.game, 'rated', m.rated,
        'started_at', m.started_at, 'finished_at', m.finished_at, 'status', m.status,
        -- NOT NULL on matches, and provenance besides: which engine produced this result.
        'engine_version', m.engine_version, 'prize_seed_commit', m.prize_seed_commit,
        'total_rounds', m.total_rounds)), '[]'::json)
      FROM h_matches m),
  'benchmark', (SELECT coalesce(json_agg(json_build_object(
        'match_id', b.match_id, 'agent', a.public_id, 'game', b.game, 'result', b.result,
        'decisions', b.decisions, 'legal', b.legal, 'illegal', b.illegal,
        'fallbacks', b.fallbacks, 'timeouts', b.timeouts, 'transport_errors', b.transport_errors,
        'latency_sum_ms', b.latency_sum_ms, 'latency_min_ms', b.latency_min_ms,
        'latency_max_ms', b.latency_max_ms, 'tokens', b.tokens,
        'prompt_tokens', b.prompt_tokens, 'completion_tokens', b.completion_tokens,
        'reasoning_tokens', b.reasoning_tokens, 'cached_tokens', b.cached_tokens,
        'estimated_cost', b.estimated_cost,
        'observed_provider', b.observed_provider, 'observed_model', b.observed_model)), '[]'::json)
      FROM agent_match_benchmark b
      JOIN h_agents a ON a.id = b.agent_id
      JOIN h_matches m ON m.public_id = b.match_id),
  'model_calls', (SELECT coalesce(json_agg(json_build_object(
        'match_id', c.match_id, 'agent', a.public_id, 'round', c.round, 'bound', c.bound,
        'provider', c.provider, 'model', c.model, 'upstream_host', c.upstream_host,
        'prompt_tokens', c.prompt_tokens, 'completion_tokens', c.completion_tokens,
        'cached_read_tokens', c.cached_read_tokens, 'cached_write_tokens', c.cached_write_tokens,
        'reasoning_tokens', c.reasoning_tokens, 'latency_ms', c.latency_ms,
        'status', c.status, 'streamed', c.streamed, 'created_at', c.created_at)), '[]'::json)
      FROM agent_model_calls c
      JOIN h_agents a ON a.id = c.agent_id
      JOIN h_matches m ON m.public_id = c.match_id),
  'decisions', (SELECT coalesce(json_agg(json_build_object(
        'match_id', d.match_id, 'agent', a.public_id, 'seq', d.seq, 'round', d.round,
        'action', d.action, 'outcome', d.outcome, 'latency_ms', d.latency_ms,
        'provider', d.provider, 'model', d.model,
        'prompt_tokens', d.prompt_tokens, 'completion_tokens', d.completion_tokens,
        'reasoning_tokens', d.reasoning_tokens, 'cached_tokens', d.cached_tokens,
        'total_tokens', d.total_tokens, 'estimated_cost', d.estimated_cost,
        'scaffold', d.scaffold, 'scaffold_unstable', d.scaffold_unstable,
        'scaffold_issue', d.scaffold_issue, 'created_at', d.created_at)), '[]'::json)
      FROM agent_match_decisions d
      JOIN h_agents a ON a.id = d.agent_id
      JOIN h_matches m ON m.public_id = d.match_id),
  'bound_decisions', (SELECT coalesce(json_agg(json_build_object(
        'match_id', bd.match_id, 'agent', a.public_id, 'round', bd.round,
        'extracted_move', bd.extracted_move, 'completion_hash', bd.completion_hash,
        'bind_receipt', bd.bind_receipt, 'created_at', bd.created_at)), '[]'::json)
      FROM agent_match_bound_decisions bd
      JOIN h_agents a ON a.id = bd.agent_id
      JOIN h_matches m ON m.public_id = bd.match_id)
);
