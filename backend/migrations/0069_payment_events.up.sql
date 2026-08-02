-- 0069_payment_events — the payment log.
--
-- Until now, "my payment did not work" could only be answered by reading server
-- logs and cross-referencing three tables. Every money flow has a fixed sequence
-- of stages, but nothing recorded which stage a given attempt actually reached, so
-- there was no way to say WHERE it broke — only that the end state was wrong.
--
-- One row per (flow, ref, stage). The flow's expected stage list lives in code
-- (internal/paymenttrace); this table records which of them were reached, when,
-- and with what outcome. A flow whose rows stop before the terminal stage IS the
-- diagnosis: the last recorded stage is the break point, and `detail` says why.
--
-- This is a DIAGNOSTIC record, not an accounting one. The ledger remains the only
-- authority on money; a missing row here means we failed to observe a step, never
-- that coins did or did not move. Nothing in a money path may fail because a write
-- to this table failed.

BEGIN;

CREATE TABLE payment_events (
    id       BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id  BIGINT NOT NULL REFERENCES users(id),
    -- flow: deposit | withdrawal | topup. ref is the flow's public id
    -- (dep_xxx / wd_xxx) or, for a top-up, the ledger idempotency key.
    flow     TEXT NOT NULL,
    ref      TEXT NOT NULL,
    stage    TEXT NOT NULL,
    -- ok      — the stage completed
    -- pending — the stage is in progress and may still complete (e.g. awaiting
    --           on-chain finality). Distinct from a break: the UI must not accuse
    --           a payment of failing while the chain is simply slow.
    -- failed  — the stage will not complete without intervention
    status   TEXT NOT NULL,
    detail   TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}',
    -- first_at is when the stage was first observed, at is the latest observation.
    -- Both are kept because the gap between them is the actual answer to "why is my
    -- deposit taking so long" — one timestamp cannot express a stage that has been
    -- pending for an hour.
    first_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- attempts counts observations that CHANGED something. A poller re-reporting an
    -- identical state is a no-op (see the upsert's WHERE clause), so this counts
    -- real transitions and retries rather than ticks of a timer.
    attempts INT NOT NULL DEFAULT 1,
    CONSTRAINT payment_events_status CHECK (status IN ('ok', 'pending', 'failed')),
    CONSTRAINT payment_events_unique UNIQUE (flow, ref, stage)
);

-- The user's own timeline, newest flow first.
CREATE INDEX idx_payment_events_user ON payment_events (user_id, at DESC);
-- One flow's stages, in the order they happened.
CREATE INDEX idx_payment_events_flow ON payment_events (flow, ref, first_at);
-- The operator's question: what is broken right now, across everyone.
CREATE INDEX idx_payment_events_failed ON payment_events (at DESC) WHERE status = 'failed';

COMMIT;
