BEGIN;

DROP INDEX IF EXISTS idx_users_privy;

ALTER TABLE users
    DROP COLUMN IF EXISTS privy_user_id,
    DROP COLUMN IF EXISTS wallet_address,
    DROP COLUMN IF EXISTS wallet_provider,
    DROP COLUMN IF EXISTS display_name,
    DROP COLUMN IF EXISTS avatar_url;

COMMIT;
