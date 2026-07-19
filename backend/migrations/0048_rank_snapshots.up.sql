-- 0048_rank_snapshots
-- Daily rank snapshots per (game, season, agent) so the leaderboard can show a
-- rank TREND (movement since the last snapshot). Written by the rank-snapshotter
-- background loop; read by the leaderboard query (trend = prev_rank - current_rank).
CREATE TABLE IF NOT EXISTS rating_rank_snapshots (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    game       TEXT NOT NULL,
    season     INT NOT NULL,
    agent_id   BIGINT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    rank       INT NOT NULL,
    taken_on   DATE NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (game, season, agent_id, taken_on)
);
CREATE INDEX IF NOT EXISTS idx_rank_snapshots_lookup
    ON rating_rank_snapshots (game, season, agent_id, taken_on DESC);
