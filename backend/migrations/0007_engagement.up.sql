-- 0007_engagement — clips, follows, and notifications (Stage 8 growth flywheel).

CREATE TABLE follows (
    user_id    BIGINT NOT NULL REFERENCES users(id),
    agent_id   BIGINT NOT NULL REFERENCES agents(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, agent_id)
);
CREATE INDEX idx_follows_agent ON follows (agent_id);

CREATE TABLE clips (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id   TEXT NOT NULL UNIQUE,                 -- clip_xxx
    match_id    BIGINT NOT NULL REFERENCES matches(id),
    trigger     TEXT NOT NULL,                        -- tie_carry|comeback|perfect_read|all_in|blowout
    round_seq   INT,
    asset_url   TEXT,                                 -- CDN url; NULL while generating
    share_count INT NOT NULL DEFAULT 0,
    shared_at   TIMESTAMPTZ,                          -- set when auto-shared (idempotent)
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (match_id, trigger)                        -- one clip per trigger per match
);
-- Trending reads only ready (asset present) clips.
CREATE INDEX idx_clips_trending ON clips (share_count DESC, created_at DESC) WHERE asset_url IS NOT NULL;

CREATE TABLE notifications (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    recipient_user_id BIGINT NOT NULL REFERENCES users(id),
    kind              TEXT NOT NULL,                  -- match_result|agent_match
    ref               TEXT NOT NULL,                  -- dedup ref, e.g. "match:m_x:ag_y"
    payload           JSONB NOT NULL DEFAULT '{}',
    read_at           TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (recipient_user_id, kind, ref)             -- idempotent per (event, recipient)
);
CREATE INDEX idx_notifications_recipient ON notifications (recipient_user_id, created_at DESC);
