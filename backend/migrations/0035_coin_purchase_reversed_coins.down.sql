BEGIN;

ALTER TABLE coin_purchases DROP COLUMN IF EXISTS reversed_coins;

COMMIT;
