-- 0013_matchmaking_queue — server-side skill-based matchmaking.
--
-- Replaces "agents grab matches[0] from a FIFO lobby" with a queue a background
-- matcher pairs by rating band (widening over wait time), excluding same-owner
-- pairings. One row per agent (re-queueing just updates it). The elo snapshot is
-- captured at enqueue time so pairing reads a single table.

CREATE TABLE matchmaking_queue (
    agent_id      BIGINT NOT NULL PRIMARY KEY REFERENCES agents(id),
    owner_user_id BIGINT NOT NULL REFERENCES users(id),
    bid           BIGINT NOT NULL,
    elo           INT    NOT NULL DEFAULT 1200,
    status        TEXT   NOT NULL DEFAULT 'waiting',  -- waiting | matched
    match_id      TEXT,                                -- match public_id, set when paired
    enqueued_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT matchmaking_queue_status_chk CHECK (status IN ('waiting', 'matched'))
);

-- The matcher scans waiting entries per bid pool, oldest first.
CREATE INDEX idx_mmq_pool ON matchmaking_queue (bid, enqueued_at) WHERE status = 'waiting';
