-- One-off backfill of agent_model_calls.upstream_host for rows written BEFORE migration 0092.
--
-- NOT a migration, and deliberately not run automatically. Provenance is a claim about where
-- a call actually went, and for historical rows that fact was never recorded — it can only be
-- reconstructed from the gateway configuration that was in effect at the time. That is
-- knowable for THIS lab and is not knowable in general, so reconstructing it has to be an
-- explicit act by someone who knows the config, not a migration that runs everywhere.
--
-- The config these rows were produced under:
--
--   LLM_GATEWAY_UPSTREAMS=anthropic=http://pyyol-toolprovider:8099,
--                         openai=http://pyyol-toolprovider:8099,
--                         groq=https://api.groq.com/openai,
--                         openrouter=https://openrouter.ai/api
--
-- So anthropic and openai rows were served by the local stand-in and are marked as such;
-- groq and openrouter rows reached the real provider. Any other provider is left at '' —
-- UNKNOWN, and therefore unpublishable — rather than guessed.
--
-- Run against a PRODUCTION database only if that database used this exact mapping. If it
-- used the shipped defaults, the correct backfill is the vendor host for every provider, and
-- this script is the wrong one.

BEGIN;

UPDATE agent_model_calls SET upstream_host = 'openrouter.ai'
 WHERE upstream_host = '' AND provider = 'openrouter';

UPDATE agent_model_calls SET upstream_host = 'api.groq.com'
 WHERE upstream_host = '' AND provider = 'groq';

-- The stand-in. Recorded rather than deleted: these calls really happened, they exercised
-- the binding path, and the coverage figures should keep counting them. What they must never
-- do is name a MODEL on a public board, and an honest host is what stops that.
UPDATE agent_model_calls SET upstream_host = 'pyyol-toolprovider:8099'
 WHERE upstream_host = '' AND provider IN ('anthropic', 'openai');

SELECT upstream_host,
       count(*) AS calls,
       count(*) FILTER (WHERE bound) AS bound,
       count(DISTINCT model) AS models
  FROM agent_model_calls
 GROUP BY upstream_host
 ORDER BY calls DESC;

COMMIT;
