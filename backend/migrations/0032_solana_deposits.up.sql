-- 0032_solana_deposits — on-chain USDC deposits (Beta wallet pipeline P2).
--
-- A user opens a deposit session for N USDC; the frontend builds a Solana Pay
-- transfer to the platform token account (ATA), tagged with a unique reference
-- pubkey. A backend listener watches that reference, and once a matching USDC
-- transfer is FINALIZED on-chain it credits the user's treasury (1 USDC = 100
-- coins, via the same ledger top-up path) and records the deposit here.
--
-- Money is credited through the double-entry ledger (idempotent on
-- "solana:<tx_signature>"); solana_deposits is the immutable, tx-keyed audit
-- record — the on-chain analogue of coin_purchases (migration 0030) — that also
-- makes re-observing a transfer a no-op.

BEGIN;

CREATE TABLE deposit_sessions (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id       TEXT NOT NULL UNIQUE,             -- dep_xxx
    user_id         BIGINT NOT NULL REFERENCES users(id),
    reference       TEXT NOT NULL UNIQUE,             -- base58 Solana Pay reference pubkey
    asset           TEXT NOT NULL DEFAULT 'USDC',
    amount_expected BIGINT NOT NULL,                  -- token base units (USDC = 6 decimals)
    coins_expected  BIGINT NOT NULL,                  -- coins to credit at the configured peg
    status          TEXT NOT NULL DEFAULT 'pending',  -- pending|detected|completed|expired|failed
    tx_signature    TEXT,                             -- set on detect/complete
    amount_received BIGINT,                           -- actual base units observed on-chain
    coins_credited  BIGINT,                           -- coins actually credited
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    CONSTRAINT deposit_sessions_amount_positive CHECK (amount_expected > 0)
);
CREATE INDEX idx_deposit_sessions_user ON deposit_sessions (user_id, created_at DESC);
-- The listener scans only still-open sessions.
CREATE INDEX idx_deposit_sessions_open ON deposit_sessions (status)
    WHERE status IN ('pending', 'detected');

CREATE TABLE solana_deposits (
    tx_signature TEXT PRIMARY KEY,                    -- dedupe / replay protection
    user_id      BIGINT NOT NULL REFERENCES users(id),
    session_id   BIGINT REFERENCES deposit_sessions(id),
    mint         TEXT NOT NULL,
    amount_base  BIGINT NOT NULL,                     -- token base units received
    coins        BIGINT NOT NULL,                     -- coins credited
    slot         BIGINT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_solana_deposits_user ON solana_deposits (user_id, created_at DESC);

COMMIT;
