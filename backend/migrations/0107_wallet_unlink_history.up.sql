-- Removing a payout wallet must LOOK gone to the developer and still BE there for audit.
--
-- Unlinking was a destructive UPDATE: verified_wallet_address, wallet_address and
-- wallet_verified_at all set to NULL on users. The address a developer had proven
-- ownership of, and had been paid to, was simply gone — and with it the only record that
-- it was ever attached to this account.
--
-- That is the wrong trade for a money surface. A payout destination is exactly the fact a
-- support ticket or a fraud review turns on later: "which wallet was this account paid to
-- in March", "did this address move between accounts", "who removed it and when". A NULL
-- answers none of it, and nothing else in the schema remembered.
--
-- So the columns on users still go NULL — that is what makes the wallet gone from the
-- developer's point of view, and every read that decides where money may be sent keeps
-- working exactly as before, with no is_deleted flag for a future query to forget. The
-- history is recorded HERE instead, append-only and beside the account rather than on it.
--
-- Append-only on purpose. A row is written when a wallet is attached and again when it is
-- removed; nothing updates or deletes. An audit trail that can be edited is not one.
CREATE TABLE IF NOT EXISTS user_wallet_history (
    id               BIGSERIAL   PRIMARY KEY,
    user_id          BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- 'linked' when a wallet was attached or proven, 'unlinked' when removed.
    action           TEXT        NOT NULL CHECK (action IN ('linked', 'unlinked')),
    -- The login hint and the PROVEN address are recorded separately because they are
    -- different claims: only a verified address could ever receive a payout, and a
    -- review needs to know which of the two it is looking at.
    wallet_address   TEXT,
    verified_address TEXT,
    provider         TEXT,
    -- When ownership had been proven, carried across so the history row stands alone.
    verified_at      TIMESTAMPTZ,
    -- Who acted. The developer's own public id for a self-service removal; an operator's
    -- id when support did it on their behalf. Free text rather than an FK: a platform
    -- token has no row in users.
    actor            TEXT        NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The two reads: one user's history newest-first (the admin panel), and "which accounts
-- has this address ever been attached to" (the fraud question that needs the address, not
-- the user, as the starting point).
CREATE INDEX IF NOT EXISTS idx_user_wallet_history_user
    ON user_wallet_history (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_wallet_history_address
    ON user_wallet_history (verified_address) WHERE verified_address IS NOT NULL;
