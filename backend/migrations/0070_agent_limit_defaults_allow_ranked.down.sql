-- Restore the pre-0070 defaults (see the up migration for why they were unusable: a new
-- agent could not afford the cheapest ranked table). Rows are untouched in both
-- directions — this only changes what a NEW agent is created with.

BEGIN;

ALTER TABLE agents
    ALTER COLUMN coin_limit_per_match SET DEFAULT 100,
    ALTER COLUMN max_bid             SET DEFAULT 100,
    ALTER COLUMN daily_loss_limit    SET DEFAULT 500,
    ALTER COLUMN session_loss_limit  SET DEFAULT 1000;

COMMIT;
