-- 0034_wallet_admin — Super Admin wallet controls (Beta wallet pipeline P4).
--
-- A single-row settings table the Super Admin toggles at runtime (deposit/
-- withdrawal switches, maintenance mode, min/max bounds, fee, confirmations) and
-- a per-wallet freeze flag for risk actions. The deposit/withdrawal services
-- consult these as a gate, so payments can be paused or a wallet frozen without a
-- redeploy. Amounts mirror the pipeline units: min_deposit_base in USDC base
-- units (6 decimals), withdrawal bounds in coins.

BEGIN;

CREATE TABLE wallet_settings (
    id                  INT PRIMARY KEY DEFAULT 1,
    deposits_enabled    BOOLEAN NOT NULL DEFAULT true,
    withdrawals_enabled BOOLEAN NOT NULL DEFAULT true,
    maintenance_mode    BOOLEAN NOT NULL DEFAULT false,
    min_deposit_base    BIGINT  NOT NULL DEFAULT 1000000,  -- 1 USDC (6 decimals)
    min_withdraw_coins  BIGINT  NOT NULL DEFAULT 500,
    max_withdraw_coins  BIGINT  NOT NULL DEFAULT 0,         -- 0 = no cap
    withdraw_fee_pct    INT     NOT NULL DEFAULT 10,
    confirmations       INT     NOT NULL DEFAULT 1,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT wallet_settings_singleton CHECK (id = 1)
);

-- Seed the singleton with defaults.
INSERT INTO wallet_settings (id) VALUES (1) ON CONFLICT DO NOTHING;

-- Per-wallet freeze: blocks deposits crediting + withdrawals for that owner.
ALTER TABLE wallets ADD COLUMN IF NOT EXISTS frozen BOOLEAN NOT NULL DEFAULT false;

COMMIT;
