-- 0011_debts — chargeback debt recovery (industry-standard negative-balance handling).
--
-- When a buyer charges back coins the agent already spent, we claw back what's
-- left and record the SHORTFALL as a receivable: a balanced double-entry move into
-- the `bad_debt` system counter-account, plus a per-agent `debts` row that
-- restricts withdrawals and auto-repays from future top-ups.

-- bad_debt is a system counter-account; like stripe_clearing/platform_revenue it
-- may carry a negative balance (the wallets CHECK from 0004 only protects
-- agent + escrow).
INSERT INTO wallets (kind, balance) VALUES ('bad_debt', 0) ON CONFLICT DO NOTHING;

CREATE TABLE debts (
    agent_id           BIGINT NOT NULL PRIMARY KEY REFERENCES agents(id),
    outstanding_coins  BIGINT NOT NULL DEFAULT 0,  -- still owed to the platform
    total_charged_back BIGINT NOT NULL DEFAULT 0,  -- lifetime, for review/metrics
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT debts_outstanding_nonneg CHECK (outstanding_coins >= 0)
);
