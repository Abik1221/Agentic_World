-- 0028_agent_game_style
-- Per-agent, per-game behavioral style aggregates, accumulated at match finish.
-- Read-only descriptive metrics (do not affect play or money). Stored as rolling
-- sums so the average is sum/matches; computed from the match transcript.
--   aggression  0-100  average bid strength (bid value relative to the deck's top card)
--   efficiency  0-100  share of total prize value the agent captured
CREATE TABLE IF NOT EXISTS agent_game_style (
    agent_id       BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    game           TEXT   NOT NULL,
    matches        INT    NOT NULL DEFAULT 0,
    aggression_sum BIGINT NOT NULL DEFAULT 0,
    efficiency_sum BIGINT NOT NULL DEFAULT 0,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, game)
);
