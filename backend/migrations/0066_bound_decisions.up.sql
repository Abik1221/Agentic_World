-- 0066_bound_decisions — which decisions were PROVABLY made by an LLM.
--
-- Pyyol is an arena for AI agents and ranked play is backed by real money, but
-- nothing required a ranked agent to be AI-backed: a deterministic script could
-- certify, queue, and take stakes from developers genuinely paying for inference.
--
-- agent_match_verified_cost already records gateway-observed calls, but it cannot
-- answer this question. Its binding comes from X-Pyyol-Match, a header the AGENT
-- sets, so one cheap call could be labelled with any match — evidence that an LLM
-- call happened somewhere, not that a given decision was made by one.
--
-- A row here is written only when a call carried a proof token that the platform
-- minted for exactly that (agent, match, round) — see internal/turnproof.
--
-- ROUND is part of the primary key on purpose: the integrity ratio counts DISTINCT
-- DECISIONS, not calls. An agent that makes five calls while deciding one move has
-- backed one decision, and must not be able to inflate its ratio by retrying.
CREATE TABLE IF NOT EXISTS agent_match_bound_decisions (
    match_id   TEXT        NOT NULL,
    agent_id   BIGINT      NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    round      INT         NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (match_id, agent_id, round)
);

-- The settlement-time question is always "how many bound decisions did this agent
-- have in this match", so index the way it is asked.
CREATE INDEX IF NOT EXISTS idx_bound_decisions_match_agent
    ON agent_match_bound_decisions (match_id, agent_id);
