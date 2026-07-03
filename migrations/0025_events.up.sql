-- 0025_events — domain event outbox (transactional outbox pattern).
--
-- Domain writes insert an event row IN THE SAME TRANSACTION as the state change
-- (see store.InsertEventTx), so an event is emitted iff the change committed. A
-- background dispatcher (internal/events) polls unpublished rows, invokes
-- idempotent handlers, and stamps published_at. This is the backbone for
-- notifications, badges, analytics, and live updates — every consumer is a
-- projection over this log, which keeps the system auditable and reproducible.

BEGIN;

CREATE TABLE events (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id    TEXT  NOT NULL UNIQUE,           -- evt_...
    type         TEXT  NOT NULL,                  -- past-tense fact, e.g. 'agent.certified'
    payload      JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,                      -- NULL until delivered to all handlers
    attempts     INT   NOT NULL DEFAULT 0          -- delivery attempts (poison-pill guard)
);

-- Fast scan of the delivery backlog (only unpublished rows are indexed).
CREATE INDEX idx_events_unpublished ON events (id) WHERE published_at IS NULL;
CREATE INDEX idx_events_type ON events (type);

COMMIT;
