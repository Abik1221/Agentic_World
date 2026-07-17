-- 0046_developer_badges — achievements awarded to a DEVELOPER (user), mirroring the
-- idempotent agent_badges table. Awarded off events (pindex.updated for rank
-- thresholds; rating.updated for streak/win-count/underdog), so at-least-once
-- delivery cannot double-award — the PK is the guard.

CREATE TABLE developer_badges (
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code       TEXT   NOT NULL,
    awarded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, code)
);
CREATE INDEX idx_developer_badges_user ON developer_badges (user_id, awarded_at);
