-- 0008_trust — anti-fraud holds/flags, the disputes workflow, and an immutable
-- audit log. Holds preserve money: a held match keeps its stakes in escrow until
-- an admin releases (pays out) or upholds (refunds).

CREATE TABLE fraud_flags (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id   BIGINT REFERENCES agents(id),
    match_id   BIGINT REFERENCES matches(id),
    type       TEXT NOT NULL,                        -- same_owner|collusion|multi_account|human_timing
    detail     TEXT,
    active     BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_fraud_flags_agent_active ON fraud_flags (agent_id) WHERE active;

CREATE TABLE payout_holds (
    match_id    BIGINT NOT NULL PRIMARY KEY REFERENCES matches(id),
    reason      TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'held',        -- held|released|refunded
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ
);

CREATE TABLE disputes (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id        TEXT NOT NULL UNIQUE,           -- dsp_xxx
    match_id         BIGINT REFERENCES matches(id),
    agent_id         BIGINT REFERENCES agents(id),
    reporter_user_id BIGINT REFERENCES users(id),
    kind             TEXT NOT NULL,                  -- collusion|payout|rigged|other
    status           TEXT NOT NULL DEFAULT 'open',   -- open|reviewing|resolved|rejected
    detail           TEXT,
    resolution       TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at      TIMESTAMPTZ
);
CREATE INDEX idx_disputes_status ON disputes (status);

-- Append-only: every fraud flag, hold, and admin action is recorded here. The
-- application never UPDATEs or DELETEs rows in this table.
CREATE TABLE audit_log (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor      TEXT NOT NULL,                        -- "system" or an admin user public id
    action     TEXT NOT NULL,
    target     TEXT,
    detail     JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_log_target ON audit_log (target, created_at DESC);
