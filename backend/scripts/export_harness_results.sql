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

-- WHICH AGENTS COUNT AS PLATFORM RESEARCH.
--
-- Not just kind='harness'. The earliest LLM-gated runs happened before that kind existed, so
-- they sit on lab agents marked 'external' — 286 attributable calls across 27 matches and 7
-- models, every one a real model answering through the gateway. Dropping them because of a
-- column that was added later would throw away most of the benchmark.
--
-- Two conditions, and both are required:
--
--   the agent is OURS — kind='harness', or a lab account (lab+…@pyyol.test). Verified before
--   widening this: all 30 such agents are lab-owned and not one belongs to a real developer.
--   Publishing somebody else's match as platform research would be the worst thing this
--   export could do, so the ownership test is on the email and not on a naming convention.
--
--   and it actually reached a MODEL — at least one bound call to a real provider host with a
--   2xx. That is what makes it research rather than lab traffic: the stand-in
--   (pyyol-toolprovider) answered thousands of well-formed calls that no model ever saw, and
--   those must never be published as a benchmark.
WITH h_agents AS (
  SELECT DISTINCT a.id, a.public_id, a.name, a.slug, a.description, a.kind
    FROM agents a
    JOIN users u ON u.id = a.owner_user_id
   WHERE (a.kind = 'harness' OR (a.kind = 'external' AND u.email LIKE 'lab+%@pyyol.test'))
     AND EXISTS (
       SELECT 1 FROM agent_model_calls c
        WHERE c.agent_id = a.id AND c.bound
          AND c.status BETWEEN 200 AND 299
          AND c.upstream_host IN ('api.groq.com', 'openrouter.ai')
     )
),
-- THE MATCH must itself be LLM-gated, not merely played by an agent that was gated once.
--
-- Selecting by agent was wrong and the published stats showed it: gemma-4-26b appeared with
-- 1,726 decisions against 164 attributable calls, and nemotron-nano-9b with 1,399 decisions
-- at a 0.2s mean think time — no model answers that fast. Those agents DID make real calls,
-- in a handful of matches; the rest of their play was against the local stand-in, and
-- importing the agent dragged all of it in. The benchmark would have published stand-in
-- decision counts and stand-in latencies as model measurements.
--
-- So the gate is per match: at least one bound, 2xx call to a real provider host inside THIS
-- match. That is the same rule the board's attribution uses, applied one level up, so the
-- exported set and the fitted set cannot disagree about what counts.
h_matches AS (
  SELECT DISTINCT m.id, m.public_id, m.game, m.rated, m.started_at, m.finished_at, m.status,
         m.engine_version, m.prize_seed_commit, m.total_rounds
    FROM matches m
    JOIN agent_match_benchmark b ON b.match_id = m.public_id
    JOIN h_agents a ON a.id = b.agent_id
   WHERE m.finished_at IS NOT NULL
     AND EXISTS (
       SELECT 1 FROM agent_model_calls c
        WHERE c.match_id = m.public_id AND c.bound
          AND c.status BETWEEN 200 AND 299
          AND c.upstream_host IN ('api.groq.com', 'openrouter.ai')
     )
     -- AND THE MODEL MUST HAVE DECIDED THE MATCH, not merely appeared in it.
     --
     -- Measured across the qualifying set:
     --
     --     game        seat decisions   llm calls
     --     mafia                   18          12
     --     goofspiel              696         506
     --     monopoly              2765          14
     --
     -- Monopoly runs to a 1000-turn cap and the seat falls back to the engine for almost
     -- every move, so those matches carried 65% of all decisions and 2.6% of the model calls.
     -- Imported, they inflated decision counts and pulled mean think-time toward zero — the
     -- published stats showed nemotron at a 0.2s average, which no model achieves.
     --
     -- The ratio is the criterion and monopoly fails it structurally, not marginally: a
     -- fallback move is a decision the ENGINE made. A match where the model chose under a
     -- quarter of the seat's moves is not evidence about that model, whatever it is evidence
     -- about. The bound share is computed per match rather than assumed per game, so a future
     -- monopoly run that IS model-driven qualifies on its own merits.
     AND (
       SELECT count(*) FILTER (WHERE c2.bound AND c2.status BETWEEN 200 AND 299
                                 AND c2.upstream_host IN ('api.groq.com', 'openrouter.ai'))::float
              -- OUR seats only. Counting every seat put a Mafia table's eleven house bots
              -- into the denominator, so a match where the model decided 12 of its own 18
              -- moves scored as if it had decided 12 of a hundred-odd — and mafia was
              -- dropped for a reason that had nothing to do with the model.
              / GREATEST(1, (SELECT sum(b2.decisions) FROM agent_match_benchmark b2
                              JOIN h_agents ha ON ha.id = b2.agent_id
                              WHERE b2.match_id = m.public_id))
         FROM agent_model_calls c2 WHERE c2.match_id = m.public_id
     ) >= 0.25
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
        -- A call whose HTTP status returned no completion cannot have bound a move, whatever
        -- the stored flag says. 335 rows in the lab carry bound=true beside a 429, 502, 400 or
        -- 401 — overwhelmingly free-tier runs that were rate-limited or refused, and exporting
        -- them as bound would credit a model with decisions it was never asked to make and
        -- inflate the one figure this benchmark exists to be trusted on.
        --
        -- Corrected here rather than in the source rows: the lab table is the record of what
        -- happened, errors included, and rewriting history to make an export clean is the
        -- wrong direction. The export is a PUBLICATION, and it states only what the response
        -- supports.
        'match_id', c.match_id, 'agent', a.public_id, 'round', c.round,
        'bound', (c.bound AND c.status BETWEEN 200 AND 299),
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
  -- The GATEWAY-VERIFIED attribution. Omitting this was a real defect: without it the stats
  -- query falls back through the SDK-reported and manifest-declared model, and the seeded
  -- benchmark advertised `claude-opus-4` and `gpt-5.2` — the lab personas' DECLARED names,
  -- for calls no such model ever answered.
  'verified_cost', (SELECT coalesce(json_agg(json_build_object(
        'match_id', v.match_id, 'agent', a.public_id, 'verified_cost', v.verified_cost,
        'calls', v.calls, 'provider', v.provider, 'model', v.model,
        'prompt_tokens', v.prompt_tokens, 'completion_tokens', v.completion_tokens,
        'total_tokens', v.total_tokens)), '[]'::json)
      FROM agent_match_verified_cost v
      JOIN h_agents a ON a.id = v.agent_id
      JOIN h_matches m ON m.public_id = v.match_id),
  -- THE MATCH ITSELF, so the benchmark can be watched and not merely read.
  --
  -- Omitting these was a real defect. The numbers were published without the games
  -- that produced them: prod held the match rows with ZERO events, so /replay
  -- answered `events: 0` and nothing could reconstruct a board, a transcript or a
  -- clock. A benchmark nobody can watch is a claim, not evidence — and this is the
  -- surface whose whole purpose is that a reader can check the platform's own
  -- results.
  --
  -- `created_at` is carried per event because it IS the pacing: a replay that
  -- reconstructs the countdown-to-finish rhythm needs the original instants, not a
  -- synthetic tick. Ordered by seq so an importer can insert without sorting.
  'events', (SELECT coalesce(json_agg(json_build_object(
        'match_id', m.public_id, 'seq', e.seq, 'type', e.type,
        'payload', e.payload, 'created_at', e.created_at
      ) ORDER BY m.public_id, e.seq), '[]'::json)
      FROM match_events e JOIN h_matches m ON m.id = e.match_id),
  -- The seating, without which a replay cannot say WHO took a turn. Agents are
  -- exported as public ids and re-resolved on import, like everything else here.
  'players', (SELECT coalesce(json_agg(json_build_object(
        'match_id', m.public_id, 'agent', a.public_id, 'seat', p.seat,
        'final_score', p.final_score)), '[]'::json)
      FROM match_players p
      JOIN h_matches m ON m.id = p.match_id
      JOIN h_agents a ON a.id = p.agent_id),
  'bound_decisions', (SELECT coalesce(json_agg(json_build_object(
        'match_id', bd.match_id, 'agent', a.public_id, 'round', bd.round,
        'extracted_move', bd.extracted_move, 'completion_hash', bd.completion_hash,
        'bind_receipt', bd.bind_receipt, 'created_at', bd.created_at)), '[]'::json)
      FROM agent_match_bound_decisions bd
      JOIN h_agents a ON a.id = bd.agent_id
      JOIN h_matches m ON m.public_id = bd.match_id)
);
