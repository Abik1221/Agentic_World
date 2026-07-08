-- 0033_withdrawal_solana — Solana USDC cash-out (Beta wallet pipeline P3).
--
-- The withdrawal workflow (request → approve → escrow-hold → pay, migration 0010)
-- is reused as-is; only the payout destination changes from a Stripe Connect
-- account to the user's Solana wallet. `chain` selects the rail and
-- `dest_wallet_address` records where a Solana payout is sent. The existing
-- `transfer_id` column stores the on-chain tx signature (same role as the Stripe
-- transfer id), and the status column gains the intermediate 'processing' /
-- 'broadcasted' states (no CHECK constraint existed, so nothing to relax).
--
-- Solana timing differs from Stripe: coins stay HELD in escrow through
-- 'processing' (claimed) and 'broadcasted' (sent), and are burned only once the
-- transaction is finalized on-chain (→ 'paid'), or released on failure.

BEGIN;

ALTER TABLE withdrawals
    ADD COLUMN IF NOT EXISTS chain               TEXT NOT NULL DEFAULT 'stripe',
    ADD COLUMN IF NOT EXISTS dest_wallet_address TEXT;

CREATE INDEX IF NOT EXISTS idx_withdrawals_broadcasted
    ON withdrawals (status) WHERE status = 'broadcasted';

COMMIT;
