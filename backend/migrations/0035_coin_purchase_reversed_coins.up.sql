-- 0035_coin_purchase_reversed_coins — track the cumulative coins already clawed
-- back per purchase so multiple partial refunds each reverse their own share.
--
-- Before this, the refund/dispute handler keyed the reversal on the PaymentIntent
-- alone, so the FIRST refund clawed its coins and every later partial refund on the
-- same PaymentIntent hit the same idempotency key and no-op'd — leaving the still
-- spendable/withdrawable coins on the account (a real loss on multi-step refunds).
-- The handler now raises this high-water mark and reverses only the delta.
BEGIN;

ALTER TABLE coin_purchases
    ADD COLUMN IF NOT EXISTS reversed_coins BIGINT NOT NULL DEFAULT 0;

COMMIT;
