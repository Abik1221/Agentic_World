-- 0006_ratings — ELO ratings per agent per season, plus an idempotency marker so
-- a finalize retry never double-applies a match's rating change.

CREATE TABLE ratings (
    agent_id       BIGINT NOT NULL REFERENCES agents(id),
    season         INT    NOT NULL,
    elo            INT    NOT NULL DEFAULT 1200,
    wins           INT    NOT NULL DEFAULT 0,
    losses         INT    NOT NULL DEFAULT 0,
    ties           INT    NOT NULL DEFAULT 0,
    coins_earned   BIGINT NOT NULL DEFAULT 0,
    current_streak INT    NOT NULL DEFAULT 0,  -- consecutive wins; resets on loss/tie
    best_streak    INT    NOT NULL DEFAULT 0,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, season)
);
CREATE INDEX idx_ratings_leaderboard ON ratings (season, elo DESC, agent_id);

-- One row per rated match: its presence means the rating change was applied.
CREATE TABLE rating_updates (
    match_id   BIGINT NOT NULL PRIMARY KEY REFERENCES matches(id),
    season     INT    NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
