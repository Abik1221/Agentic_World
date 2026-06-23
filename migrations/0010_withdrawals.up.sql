-- 0010_withdrawals — cash-out (coins → money) with a request → approve → pay
-- workflow. Coins are HELD in escrow on request and only burned on a confirmed
-- Stripe payout, so reconciliation stays exact and money is never lost in flight.
--
-- Economic model (1 coin = 1¢ face): the platform takes a sell fee, and the
-- Stripe payout fee is passed to the user — both deducted from the gross, so the
-- player only ever sees coins. Only NET WINNINGS are withdrawable (deposited /
-- bonus coins are play-only), which kills buy→withdraw arbitrage + laundering.

CREATE TABLE withdrawals (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    public_id          TEXT NOT NULL UNIQUE,            -- wd_xxx
    agent_id           BIGINT NOT NULL REFERENCES agents(id),
    user_id            BIGINT NOT NULL REFERENCES users(id),
    coins              BIGINT NOT NULL,                 -- coins withdrawn (held in escrow)
    fee_coins          BIGINT NOT NULL,                 -- platform sell fee (→ platform_revenue)
    gross_cents        BIGINT NOT NULL,                 -- coins × coin_cents
    stripe_fee_cents   BIGINT NOT NULL,                 -- estimated payout fee, passed to user
    net_cents          BIGINT NOT NULL,                 -- actually paid to the user's bank
    connect_account_id TEXT,
    status             TEXT NOT NULL DEFAULT 'requested', -- requested|approved|paid|rejected|failed
    transfer_id        TEXT,                            -- Stripe transfer/payout id
    reason             TEXT,
    requested_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at        TIMESTAMPTZ,
    CONSTRAINT withdrawals_coins_positive CHECK (coins > 0)
);
CREATE INDEX idx_withdrawals_agent ON withdrawals (agent_id, requested_at DESC);
CREATE INDEX idx_withdrawals_status ON withdrawals (status);
