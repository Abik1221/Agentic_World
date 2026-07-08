-- 0031_privy_identity — Privy as the auth front door (Beta wallet pipeline P1).
--
-- Privy owns authentication (social + email + external/embedded Solana wallets);
-- the backend verifies Privy's access token, then find-or-creates a user keyed on
-- the Privy user id and mints the EXISTING dashboard JWT. So this migration only
-- adds the identity columns Privy supplies. No agent is created — a Privy user is
-- a first-class human owner who MAY later become a developer (agent optional),
-- unlike the X-claim / email-password flows which couple user+agent creation.
--
-- wallet_address / wallet_provider here are DISPLAY HINTS captured at login (the
-- user's connected/embedded wallet). Withdrawal destinations are re-validated at
-- payout time (pipeline P3), so a hint is never load-bearing for moving money.

BEGIN;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS privy_user_id   TEXT,
    ADD COLUMN IF NOT EXISTS wallet_address  TEXT,
    ADD COLUMN IF NOT EXISTS wallet_provider TEXT,   -- phantom|solflare|backpack|privy(embedded)|...
    ADD COLUMN IF NOT EXISTS display_name    TEXT,
    ADD COLUMN IF NOT EXISTS avatar_url      TEXT;

-- One Privy identity maps to at most one owner. Partial unique index so the many
-- existing X-claim / email-password users (privy_user_id IS NULL) are unaffected.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_privy
    ON users (privy_user_id)
    WHERE privy_user_id IS NOT NULL;

COMMIT;
