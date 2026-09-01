-- 0092_model_calls_upstream_host — record WHICH upstream actually answered a model call.
--
-- The model board's whole claim is that it ranks models rather than assertions about
-- models, and it enforces that with `bound = true`: the gateway saw the provider return
-- this model, on a call tied to a specific decision.
--
-- There was a hole underneath it. `provider` and `model` are read from the REQUEST, and
-- `LLM_GATEWAY_UPSTREAMS` decides where that request is actually sent. Point anthropic at
-- a local stand-in — which every lab run does — and the gateway faithfully records
-- `anthropic / claude-opus-4`, bound, with usage, for a call that never left the machine.
-- Nothing on the row distinguishes it from a real one afterwards.
--
-- In this database that is not hypothetical: 1,601 of 1,829 bound calls were served by the
-- lab stand-in, under 25 model names including "lab-alpha-8b" and "claude-opus-4". Ranking
-- those as models would be exactly the fraud the gateway exists to prevent, committed by
-- the tool built to detect it.
--
-- So the host is recorded at proxy time. It is the only fact that settles the question, it
-- cannot be reconstructed later, and storing it means the board can filter on where a call
-- WENT rather than on a hardcoded list of vendor names.
ALTER TABLE agent_model_calls
    ADD COLUMN IF NOT EXISTS upstream_host TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN agent_model_calls.upstream_host IS
    'Host of the upstream the gateway actually dialled, from LLM_GATEWAY_UPSTREAMS. Empty '
    'for rows written before 0092 — treat those as UNKNOWN provenance, never as external.';

-- The board reads bound calls by model; it now also filters by host, so keep the host in
-- the same index rather than forcing a heap lookup per row.
CREATE INDEX IF NOT EXISTS idx_model_calls_bound_host
    ON agent_model_calls (upstream_host, model, created_at DESC) WHERE bound;
