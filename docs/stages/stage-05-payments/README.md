# Stage 5 — Payments (Stripe)

> **Goal:** real money buys coins (Tier 1), idempotently and reconcilably; lay the
> Stripe Connect groundwork for funded-prize payouts (Tier 2). This completes the
> MVP loop: **register → buy coins → play → win → watch replay.**

**Maps to:** Plan Phase 0 W3 (Tier 1) + Phase 1 W6 (Tier 2 onboarding); Payments §7.
**Depends on:** Stage 4 (ledger — top-ups credit it).
**Unblocks:** funded tournaments (Stage 8+), Tier 2 payouts.

## Scope
**In:** Stripe Checkout for coin packs, signed+idempotent webhook processing →
ledger top-up, transaction history, Stripe Connect Express onboarding (KYC link),
hourly Stripe↔ledger reconciliation. **Tier 1 only at launch: coins in, no cash
out.**
**Out:** real-money wagering / cash-out (Tier 3 — licensed, much later).

## Design references
- [data-model.md](../../architecture/data-model.md) (`stripe_events`, `users.stripe_*`, ledger)
- [security.md](../../architecture/security.md) (webhook verification, idempotency)
- [api-surface.md](../../architecture/api-surface.md) (`/v1/wallet/topup`, `/v1/payouts/onboard`, `/v1/webhooks/stripe`)

## Coin packs (config)
`$1=100`, `$5=550 (+10%)`, `$20=2400 (+20%)`, `$50=6500 (+30%)` — defined in config,
not hardcoded in handlers.

## Tasks
- [ ] Migration: `stripe_events` (idempotent webhook log); `users.stripe_customer_id`/`stripe_connect_id`.
- [ ] `internal/payments`: create Checkout session for a pack (`POST /v1/wallet/topup`, user scope) → returns hosted URL; metadata carries `user_id` + pack + coin amount.
- [ ] Webhook `POST /v1/webhooks/stripe`: verify `Stripe-Signature`; **persist event by id first** (idempotency); on `checkout.session.completed`/`payment_intent.succeeded` → ledger `topup` txn (debit `stripe_clearing`, credit agent/owner wallet) with `idem topup:{stripe_event_id}`.
- [ ] Reconcile job (hourly): Stripe charges ↔ `stripe_events` ↔ ledger top-ups; replay any missed/failed webhook; alert on unmatched.
- [ ] Stripe Connect Express: `POST /v1/payouts/onboard` (user scope) → KYC onboarding link; store `stripe_connect_id`; **payout execution gated behind the validation gate** (Stage 4) — Tier 2 funded prizes only.
- [ ] Refund/chargeback handling: on dispute/refund event, reverse the ledger top-up idempotently; flag account; never allow negative wallet (claw back from balance, else negative-protected debt record).
- [ ] Hosted error/success return pages config; never render card forms ourselves.

## Data model delta
`stripe_events`; `users.stripe_customer_id`, `users.stripe_connect_id`.

## API delta
`POST /v1/wallet/topup` (user), `POST /v1/payouts/onboard` (user),
`POST /v1/webhooks/stripe` (inbound, verified).

## Acceptance criteria
- A user buys a pack via Checkout (test mode); on `completed` webhook, coins are
  credited **exactly once** even if Stripe redelivers the event (idempotent).
- A webhook with a bad signature is rejected (`400`) and not processed.
- Reconciliation on a seeded ledger finds **zero** unmatched charges; a simulated
  missed webhook is detected and replayed to credit coins.
- A refund event reverses the corresponding top-up idempotently; balances and
  ledger remain consistent (reconciliation still zero-drift).
- Connect onboarding returns a working KYC link and stores `stripe_connect_id`;
  no payout can execute unless the Stage 4 validation gate passes.

## Test plan
- Integration with Stripe **test mode** + the Stripe CLI to fire/redeliver webhooks
  (assert single credit on duplicates).
- Signature verification (valid/invalid/expired).
- Reconciliation: drop a webhook → reconcile credits it; double-deliver → single effect.
- Refund/chargeback reversal correctness.

## Observability
- `topups_total`, `topup_coins_total`, `stripe_webhook_failures_total`,
  `reconcile_unmatched_total` (=0). Alert on webhook failure spikes / unmatched > 0.

## Security
- Webhook signature HMAC + event-id idempotency; secrets via env; never store
  PANs; payout behind validation gate; Tier separation explicit (no cash-out at
  launch). **Consult counsel + verify Stripe terms before enabling Tier 2/3.**

## Risks
- Stripe account risk → launch Tier 1 (play-money) only; free-entry for Tier 2;
  Tier 3 on a separate licensed track (Risk Register).
- Webhook loss → idempotent log + reconciliation guarantees eventual correctness.

## Definition of Done
Buy-coins works idempotently and reconciles to zero drift; Connect onboarding
ready (payouts gated); refund path correct; MVP loop complete end-to-end.
