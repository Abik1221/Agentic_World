-- 0057_agent_match_verified_cost — per-(match, agent) GATEWAY-VERIFIED LLM cost.
--
-- Distinct from agent_match_benchmark.estimated_cost, which is agent-SELF-REPORTED
-- (meter_source=sdk) and therefore gameable. This table is fed only by the Pyyol
-- Gateway (server-observed, unfakeable), so the P-Index cost-efficiency dimension
-- can score cost-to-win WITHOUT reintroducing a gameable signal. Accumulated per LLM
-- call the gateway proxies (calls counts how many were observed).
CREATE TABLE IF NOT EXISTS agent_match_verified_cost (
    match_id      TEXT             NOT NULL,
    agent_id      BIGINT           NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    verified_cost DOUBLE PRECISION NOT NULL DEFAULT 0,
    calls         INT              NOT NULL DEFAULT 0,
    updated_at    TIMESTAMPTZ      NOT NULL DEFAULT now(),
    PRIMARY KEY (match_id, agent_id)
);
