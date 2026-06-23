-- 0001_system_wallets — the first migration.
-- Establishes the wallets table and seeds the three system wallets the
-- double-entry ledger (Stage 4) requires as counter-accounts. The agent_id FK to
-- agents(id) is intentionally deferred: agents is created in Stage 1 and the FK +
-- per-agent wallet rows are added in Stage 4. Keeping this migration self-contained
-- lets Stage 0 stand alone and gives the readiness probe a schema_migrations row.

CREATE TABLE IF NOT EXISTS wallets (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id   BIGINT UNIQUE,                       -- NULL for system wallets; FK added in Stage 4
    kind       TEXT        NOT NULL DEFAULT 'agent', -- agent | stripe_clearing | platform_revenue | escrow
    balance    BIGINT      NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT wallets_balance_nonneg CHECK (balance >= 0)
);

-- One row per system wallet kind (idempotent on re-run via the partial unique index).
CREATE UNIQUE INDEX IF NOT EXISTS idx_wallets_system_kind
    ON wallets (kind) WHERE agent_id IS NULL;

INSERT INTO wallets (kind, balance)
VALUES ('stripe_clearing', 0), ('platform_revenue', 0), ('escrow', 0)
ON CONFLICT DO NOTHING;
