-- 0079_model_calls — the server-observed record of every model call routed through the
-- Pyyol LLM Gateway.
--
-- WHY THIS TABLE EXISTS AT ALL. Before it, the only answer to "which model did this agent
-- use" was a string the agent asserted about itself: 0 of 2,579 recorded decisions carried
-- verified attribution. A model leaderboard built on that ranks whoever writes the most
-- flattering manifest. Every row here was observed by the platform while proxying the call,
-- so it is evidence rather than testimony.
--
-- WHY UNBOUND CALLS ARE STORED TOO. `bound` records whether the call's per-turn proof
-- verified. It would be tempting to keep only the verified ones, but the ratio of bound to
-- total is exactly the COVERAGE figure a verified leaderboard has to publish — and without
-- it a cost-per-win board is actively perverse, because an agent that routes 5% of its
-- calls and does the rest elsewhere looks like the cheapest agent on the platform. Keeping
-- both is what makes the denominator visible.
--
-- NOT A PROMPT LOG. Deliberately no request or response bodies. The gateway carries
-- developers' prompts and their own provider keys; storing either would make Pyyol a
-- credential-and-IP custodian and one breach catastrophic. Counts, timings and model
-- identity are everything the measurement needs.

BEGIN;

CREATE TABLE IF NOT EXISTS agent_model_calls (
    id              BIGSERIAL PRIMARY KEY,
    agent_id        BIGINT      NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    -- match_id / round are what the agent CLAIMED. Meaningless unless bound is true; the
    -- proof is what makes them trustworthy, not the headers they arrived in. Nullable
    -- because a call outside any match (a warm-up, a developer testing) is still worth
    -- recording for cost and latency.
    match_id        TEXT,
    round           INTEGER,
    -- bound: the per-turn proof verified for exactly this (agent, match, round). The single
    -- column that separates a measurement from a claim.
    bound           BOOLEAN     NOT NULL DEFAULT false,
    provider        TEXT        NOT NULL DEFAULT '',
    -- The model the PROVIDER said it served, which resolves an alias like
    -- "claude-opus-4-latest" to the concrete version a leaderboard must compare.
    model           TEXT        NOT NULL DEFAULT '',
    prompt_tokens       INTEGER NOT NULL DEFAULT 0,
    completion_tokens   INTEGER NOT NULL DEFAULT 0,
    -- Cache reads and WRITES are separate because they are billed differently: Anthropic
    -- charges 1.25x for a write and 0.1x for a read. Folding them together understates the
    -- cost of every caching agent by an amount that varies with how it caches, which
    -- silently corrupts cost-efficiency comparison between models.
    cached_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cached_write_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens    INTEGER NOT NULL DEFAULT 0,
    -- The upstream call alone, measured by the platform. Distinct from turn latency, which
    -- also contains the developer's prompt building and parsing — charging that to their
    -- model would blame a provider for a developer's slow code.
    latency_ms      BIGINT      NOT NULL DEFAULT 0,
    status          INTEGER     NOT NULL DEFAULT 0,
    streamed        BOOLEAN     NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The model board's read path: bound calls for a model over a window.
CREATE INDEX IF NOT EXISTS idx_model_calls_model_bound
    ON agent_model_calls (model, created_at DESC) WHERE bound;

-- Coverage per agent per match: how much of a match was actually verified.
CREATE INDEX IF NOT EXISTS idx_model_calls_match
    ON agent_model_calls (match_id, agent_id) WHERE match_id IS NOT NULL;

COMMIT;
