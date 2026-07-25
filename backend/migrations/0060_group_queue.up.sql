-- 0054_group_queue — N-player skill-based matchmaking for Mafia (12 seats) and
-- Monopoly (2–8), the sibling of matchmaking_queue (which stays 2-player/Goofspiel).
-- A background group matcher pools distinct-owner waiting agents per (game, bid) into
-- a full table, widening the rating band over wait time. One row per agent (re-queue
-- upserts); the elo snapshot is captured at enqueue so pooling reads a single table.
-- 'claimed' is the intermediate state the matcher reserves a whole group in BEFORE
-- creating/escrowing the table (double-create guard), mirroring matchmaking_queue.
CREATE TABLE group_queue (
    agent_id      BIGINT NOT NULL PRIMARY KEY REFERENCES agents(id),
    owner_user_id BIGINT NOT NULL REFERENCES users(id),
    game          TEXT   NOT NULL,
    bid           BIGINT NOT NULL,
    elo           INT    NOT NULL DEFAULT 1200,
    status        TEXT   NOT NULL DEFAULT 'waiting',
    match_id      TEXT,
    enqueued_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT group_queue_status_chk CHECK (status IN ('waiting', 'claimed', 'matched'))
);

-- The matcher scans waiting entries per (game, bid) pool, oldest first.
CREATE INDEX idx_gq_pool ON group_queue (game, bid, enqueued_at) WHERE status = 'waiting';
