-- 0039_wallet_ownership — prove control of the payout wallet before withdrawing.
--
-- Until now a withdrawal's destination was users.wallet_address — an UNVERIFIED
-- login hint from Privy. A mistaken/injected hint (or, before the W1 fix, a taken
-- over account) could send USDC to the wrong wallet irreversibly. Now the owner
-- must sign a server-issued nonce with the destination wallet's key; only a wallet
-- whose ownership is proven (verified_wallet_address) can receive a payout.
BEGIN;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS verified_wallet_address TEXT,
    ADD COLUMN IF NOT EXISTS wallet_verified_at      TIMESTAMPTZ;

-- One outstanding challenge per user (a new challenge overwrites the old).
CREATE TABLE IF NOT EXISTS wallet_verify_challenges (
    user_public_id TEXT PRIMARY KEY,
    wallet_address TEXT NOT NULL,
    nonce          TEXT NOT NULL,
    expires_at     TIMESTAMPTZ NOT NULL
);

COMMIT;
