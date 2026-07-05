-- 0004_ledger — double-entry ledger + per-agent wallets.
-- Completes the wallets table (the agent FK was deferred in 0001 until agents
-- existed), adds the transaction/entry tables, and backfills a wallet for every
-- existing agent. The ledger is the ONLY coin-mover (see docs/architecture/data-model.md).

-- 1. Wire the deferred agent FK now that agents exists, and backfill wallets.
ALTER TABLE wallets
    ADD CONSTRAINT fk_wallets_agent FOREIGN KEY (agent_id) REFERENCES agents(id);

INSERT INTO wallets (agent_id, kind, balance)
SELECT a.id, 'agent', 0 FROM agents a
WHERE NOT EXISTS (SELECT 1 FROM wallets w WHERE w.agent_id = a.id);

-- 2. Refine the non-negative invariant. The original blanket CHECK is replaced so
--    it protects the balances that MUST never go negative — agent wallets (an
--    agent can never overspend) and escrow (we can never pay out more than was
--    staked) — while leaving the system counter-accounts (platform_revenue,
--    stripe_clearing) free to carry the normal +/- flows of double-entry
--    bookkeeping (e.g. a non-prod mint draws stripe_clearing negative).
ALTER TABLE wallets DROP CONSTRAINT IF EXISTS wallets_balance_nonneg;
ALTER TABLE wallets ADD CONSTRAINT wallets_protected_balance_nonneg
    CHECK (kind NOT IN ('agent', 'escrow') OR balance >= 0);

-- 3. Double-entry transactions and their balanced entries.
CREATE TABLE ledger_transactions (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id       TEXT NOT NULL UNIQUE,                 -- txn_xxx
    kind            TEXT NOT NULL,                        -- topup|stake|settle|refund
    idempotency_key TEXT NOT NULL UNIQUE,                 -- e.g. "settle:m_777" — blocks double-spend
    metadata        JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE ledger_entries (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    txn_id     BIGINT NOT NULL REFERENCES ledger_transactions(id),
    wallet_id  BIGINT NOT NULL REFERENCES wallets(id),
    amount     BIGINT NOT NULL,                           -- signed; +credit / -debit
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_entries_wallet ON ledger_entries(wallet_id);
CREATE INDEX idx_entries_txn ON ledger_entries(txn_id);
