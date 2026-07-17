-- 0044_developer_identity — give developers a public identity + a social graph.
--
-- A developer (a `users` row) gains a public @handle (username), an optional country,
-- and a self-declared segment for the segmented leaderboards (Individuals / Students /
-- Startups / Companies). username is CITEXT UNIQUE so @Alice and @alice collide.
-- developer_follows is the developer↔developer graph (distinct from the existing
-- user→agent follows in `follows`).

ALTER TABLE users ADD COLUMN username CITEXT UNIQUE;
ALTER TABLE users ADD COLUMN country  TEXT;
ALTER TABLE users ADD COLUMN segment  TEXT NOT NULL DEFAULT 'individual';  -- individual|student|startup|company

CREATE TABLE developer_follows (
    follower_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    followee_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (follower_user_id, followee_user_id),
    CHECK (follower_user_id <> followee_user_id)
);
CREATE INDEX idx_developer_follows_followee ON developer_follows (followee_user_id);
