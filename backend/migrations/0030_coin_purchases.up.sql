-- 0030_coin_purchases — record each settled top-up keyed by its Stripe
-- PaymentIntent so a later refund/dispute can be clawed back. Refund and dispute
-- webhook objects carry payment_intent but NOT the coins/user/agent metadata
-- (Stripe does not copy PaymentIntent metadata onto the charge/dispute), so the
-- reversal path had no way to know how many coins to reverse or from whom. This
-- table is the local mapping we control, written at credit time.
CREATE TABLE coin_purchases (
    payment_intent  TEXT PRIMARY KEY,        -- Stripe pi_… (links session, charge, dispute)
    session_id      TEXT NOT NULL,
    user_public_id  TEXT NOT NULL,           -- treasury that was credited
    agent_public_id TEXT,                    -- agent the top-up was bought for (debt attribution)
    coins           BIGINT NOT NULL,
    amount_cents    BIGINT NOT NULL,         -- charged amount (for proportional partial refunds)
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
