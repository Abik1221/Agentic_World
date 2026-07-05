-- 0005_stripe_events — the idempotent inbound-webhook log.
-- Every Stripe event is persisted by its id BEFORE we act on it, so a redelivery
-- (Stripe delivers at-least-once) is recognised and never double-credits coins.
-- The users.stripe_customer_id / stripe_connect_id columns already exist (0002).

CREATE TABLE stripe_events (
    id           TEXT PRIMARY KEY,                  -- Stripe event id (the idempotency key)
    type         TEXT NOT NULL,
    payload      JSONB NOT NULL,
    processed_at TIMESTAMPTZ,                        -- NULL until fully handled
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Find unprocessed events fast (webhook-failure replay / reconciliation).
CREATE INDEX idx_stripe_events_unprocessed ON stripe_events (created_at)
    WHERE processed_at IS NULL;
