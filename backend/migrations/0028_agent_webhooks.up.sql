-- 0028_agent_webhooks — durable webhook delivery queue for the push protocol.
--
-- The engine never blocks on /event + /game-end. Instead of firing best-effort
-- goroutines, the drive loops ENQUEUE a delivery row here, and a central
-- background dispatcher (internal/webhook) delivers it: signed (HMAC), at-least-
-- once, with exponential backoff and a per-endpoint health circuit breaker so a
-- dead endpoint is skipped rather than hammered. This is the Stripe-style signed-
-- webhook pattern, made durable + horizontally safe (rows survive restarts; a
-- single delivery is claimed by exactly one worker via next_attempt_at + tx).

BEGIN;

CREATE TABLE agent_webhook_deliveries (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id       TEXT  NOT NULL UNIQUE,               -- whk_...
    agent_public_id TEXT  NOT NULL,                      -- resolve endpoint fresh at send time
    kind            TEXT  NOT NULL,                      -- 'event' | 'game-end'
    game            TEXT  NOT NULL,
    match_public_id TEXT  NOT NULL,
    seq             INT   NOT NULL DEFAULT 0,             -- event ordering (0 for game-end)
    event_type      TEXT  NOT NULL DEFAULT '',
    payload         JSONB NOT NULL DEFAULT '{}',
    attempts        INT   NOT NULL DEFAULT 0,             -- delivery attempts (poison-pill guard)
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),   -- backoff gate; row is due when <= now()
    delivered_at    TIMESTAMPTZ,                          -- NULL until delivered (or given up)
    last_error      TEXT  NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Idempotent enqueue: a drive loop re-reading the same (match, kind, seq) — e.g.
-- after re-polling state — can INSERT ... ON CONFLICT DO NOTHING without dupes.
CREATE UNIQUE INDEX uq_webhook_dedupe
    ON agent_webhook_deliveries (agent_public_id, match_public_id, kind, seq);

-- Delivery backlog scan: only rows still awaiting delivery are indexed, ordered
-- by when they next come due.
CREATE INDEX idx_webhook_pending
    ON agent_webhook_deliveries (next_attempt_at) WHERE delivered_at IS NULL;

COMMIT;
