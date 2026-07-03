-- 0015_subscriptions — Arena Pass recurring billing (Stripe Billing).

CREATE TABLE subscriptions (
    id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id                 BIGINT NOT NULL UNIQUE REFERENCES users(id),
    stripe_customer_id      TEXT,
    stripe_subscription_id  TEXT UNIQUE,
    plan_key                TEXT NOT NULL DEFAULT 'arena_pass',
    status                  TEXT NOT NULL DEFAULT 'inactive', -- inactive|active|past_due|canceled
    current_period_end      TIMESTAMPTZ,
    monthly_coins           BIGINT NOT NULL DEFAULT 1000,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_subscriptions_status ON subscriptions (status);

-- Idempotent monthly coin grants keyed by Stripe invoice id.
CREATE TABLE subscription_grants (
    id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id           BIGINT NOT NULL REFERENCES users(id),
    stripe_invoice_id TEXT NOT NULL UNIQUE,
    coins             BIGINT NOT NULL,
    granted_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
