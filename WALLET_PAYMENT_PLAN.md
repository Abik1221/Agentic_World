# Onavion Beta — Privy Auth + Solana USDC Wallet Pipeline (Implementation Plan)

Status: proposed · Target: Beta · Chain: Solana · Asset: USDC (USDT later)

## 0. Reality check (what already exists)

The spec assumes a green-field FastAPI service. It is not. The backend is **Go**
(`github.com/agent-arena/arena`, chi router, pgx/Postgres, golang-migrate, HS256
session JWT) and it **already implements the entire internal economy** behind
clean ports/adapters — currently wired to **Stripe**. We are not building a wallet
system; we are **replacing the two outer edges** (auth front door + fiat on/off
ramp) and reusing everything in between.

| Concern | Already built | File(s) |
|---|---|---|
| Session JWT (issue/verify) | ✅ HS256, scopes agent/user/platform | `internal/auth/jwt.go`, `auth.go` |
| Pluggable credential resolver | ✅ `KeyResolver`, `PlatformVerifier` | `internal/auth/auth.go`, `platform.go` |
| User find-or-create | ✅ (in claim flow today) | `internal/identity/service.go` |
| Internal balances (treasury + agent + escrow) | ✅ | `internal/wallet/*`, mig `0016_user_wallets` |
| Immutable double-entry ledger + reconcile | ✅ | `internal/ledger/*`, mig `0004_ledger` |
| Deposit → credit (coin packs, idempotent webhook, refund clawback) | ✅ Stripe | `internal/payments/*`, mig `0030_coin_purchases` |
| Withdrawal (request→approve→pay, escrow hold, net-winnings-only, anti-fraud/debt gates, audit) | ✅ Stripe | `internal/payout/*`, mig `0010_withdrawals`, `0011_debts` |
| Gateway/Transferrer ports (Dev + Stripe adapters) | ✅ | `payments/dev_gateway.go`+`stripe_gateway.go`, `payout/transfer.go` |
| Secret encryption at rest | ✅ | `internal/secretbox` |
| Admin API + Ed25519 platform authz | ✅ | `internal/adminapi`, `internal/platformsign` |
| Frontend shells | ✅ Next.js | `Frontend/app/{wallet,withdrawals,payouts,login}`, `components/{wallet,auth,payments}` |

**Consequence:** Solana becomes two new **adapters** implementing existing ports,
plus one **listener worker** and a **Privy verifier**. The ledger and game
economy are untouched.

---

## 1. Decisions (locked defaults, flagged where they need your sign-off)

1. **Stack = Go, not FastAPI.** The spec's "FastAPI + Postgres + Redis" describes
   intent, not this repo. We implement in the existing Go service. (Postgres +
   Redis already present.)
2. **Coin peg stays `1 coin = 1¢`** (existing `CoinCents` economic model). A USDC
   deposit converts at **1 USDC = 100 coins**. This preserves every line of the
   ledger, fee, payout, and game-economy code. Users see "credits"; the peg is
   invisible. → **needs sign-off** (alternative: 1 credit = 1 USDC, which forces a
   re-peg of the whole economy — not recommended for Beta).
3. **Keep Stripe code, disable it via config.** Select Solana adapters at wiring
   time in `cmd/server`. Matches the spec's "modular — add methods later" and lets
   us fall back. → **needs sign-off** (alternative: delete Stripe now).
4. **Deposit identification = Solana Pay reference key** (a unique read-only
   pubkey attached to the transfer instruction), funds landing in **one shared
   platform USDC ATA**. Avoids per-user address custody sprawl and works with both
   Privy embedded wallets and external wallets. → **needs sign-off** (alternative:
   per-user derived deposit addresses — more custody surface).
5. **Withdrawal custody = single platform hot wallet**, key encrypted with
   `secretbox`, balance-capped, behind the existing approval-threshold + daily-limit
   + anti-fraud gates in `payout`. Sweep deposits from the ATA to the hot/cold
   wallet on a schedule.
6. **Only net winnings are withdrawable** — existing rule, kept. Deposited credits
   are play-only (kills buy→withdraw laundering). This is already enforced in
   `payout.Repo.Withdrawable`.

---

## 2. Target architecture

```
                         ┌──────────── Frontend (Next.js) ────────────┐
                         │ PrivyProvider (social + wallet + embedded)  │
                         │ /login  /wallet  /withdrawals  /settings    │
                         └───────────────┬─────────────────────────────┘
        Privy access token (ES256)       │            Solana tx (USDC → platform ATA, w/ reference)
                                         ▼                         │
   POST /v1/auth/privy ──► PrivyVerifier (JWKS) ──► identity.UpsertFromPrivy ──► auth.JWT (HS256, unchanged)
                                         │                         │
                    ┌────────────────────┴─────────────┐          │  (on-chain)
                    ▼ (all existing, unchanged)         │          ▼
             wallet.Service ── ledger.Service    payout.Service   Solana RPC (mainnet/devnet)
                    ▲  (Coiner port)                    ▲              │
                    │                                   │ Transferrer  │ getSignaturesForAddress(reference)
             solanadeposit.Listener  ◄─────────────────┼──────────────┘
             (worker: watch → confirm → credit)   SolanaTransferrer (hot wallet: build/sign/broadcast)
```

New Go packages: `internal/blockchain` (Solana RPC client + confirmation
tracking), `internal/solanadeposit` (deposit sessions + listener), `internal/auth`
gains `privy.go`. New adapters inside `payments` (deposit credit) and `payout`
(Solana transfer).

---

## 3. Workstream A — Privy authentication (front door only)

**Goal:** log in with Google/email/GitHub/X/Discord/wallet, auto-create the
account, ≤60 s onboarding — without changing the rest of the system's auth.

**Backend**
- Add `internal/auth/privy.go`: `PrivyVerifier` that fetches Privy's JWKS,
  verifies the Privy access token (ES256, `iss`/`aud`/exp), and returns the
  `privy_user_id` + linked accounts + wallet address. Reuse `golang-jwt/jwt/v5`
  (add a JWKS helper, e.g. `MicahParks/keyfunc`, or hand-roll ~40 lines).
- Add `identity.UpsertFromPrivy(ctx, claims) (userPublicID, error)` — find-or-create
  the user keyed on `privy_user_id`; store wallet address, provider, social handle,
  avatar, display name. Then mint the **existing** `auth.JWT` for the session. The
  whole downstream system keeps using the current session token — Privy is a
  drop-in front door.
- Add `POST /v1/auth/privy` (handler in `identity` or a small `authhttp`): body =
  Privy access token → verify → upsert → `{ token, user }`.
- Keep password/claim login paths working (agents still use API keys; humans can
  use Privy). No change to `auth.Middleware` / `RequireScope`.

**Frontend**
- Add `@privy-io/react-auth`. Wrap `Frontend/app/layout` in `PrivyProvider`
  (appId from `NEXT_PUBLIC_PRIVY_APP_ID`), configure login methods (google, email,
  github, twitter, discord) + external wallets (phantom, solflare, backpack) +
  embedded Solana wallet on login.
- Rewrite `app/login` + `components/auth` to trigger Privy's modal; on success POST
  the Privy access token to `/v1/auth/privy`, store the returned session token,
  redirect to `/dashboard`.

**Migration** `0031_privy_identity.up.sql`: add `users.privy_user_id TEXT UNIQUE`,
`wallet_address TEXT`, `wallet_provider TEXT`, plus `display_name/avatar_url` if
absent; index on `privy_user_id`. (Consider a `user_linked_accounts` table for
multi-provider, but one active wallet in Beta.)

**Config/secrets:** `PRIVY_APP_ID`, `PRIVY_APP_SECRET`, `PRIVY_JWKS_URL` (or
verification key). Never expose the secret to the frontend.

**Ships:** one-tap login + auto account creation.

---

## 4. Workstream B — Solana deposit read-path (USDC → credits)

**Goal:** user sends USDC on Solana; credits appear automatically after
confirmation, never before.

**New `internal/blockchain` (Solana RPC client)**
- Wrap an RPC client (recommend `github.com/gagliardetto/solana-go`). Methods:
  `GetSignaturesForAddress(reference)`, `GetTransaction(sig)`, `Confirmations(sig)`,
  and (for B/C) SPL-token transfer building/parsing. Config: `SOLANA_RPC_URL`,
  `SOLANA_CLUSTER` (devnet first), `USDC_MINT`, `PLATFORM_USDC_ATA`,
  `DEPOSIT_CONFIRMATIONS` (default e.g. 1 finalized/32).

**New `internal/solanadeposit`**
- `DepositSession` service: `Create(userPublicID, amountUSDC)` → generates a fresh
  **reference keypair pubkey**, stores a pending session (id, user, reference,
  asset, amount_expected, status, expires_at), returns `{ referencePubkey, ata,
  amountExpected, solanaPayURL }` for the frontend to build a Solana Pay transfer.
- `Listener` worker (runs as its own `cmd/deposit-listener` or a guarded
  background goroutine in `cmd/server`): polls `getSignaturesForAddress(reference)`
  for each open session (or a webhook from an indexer like Helius later) →
  `getTransaction` → verify: mint == USDC, destination == platform ATA,
  amount ≥ expected, reference present, confirmations ≥ threshold → transition
  session `pending→detected→confirming→completed`.
- On completion: credit the user's treasury wallet via the **existing
  `payments.Coiner`** (or `wallet.Service` directly) at 1 USDC = 100 coins, write
  the immutable ledger entry (ledger type `Deposit`), and record a
  `solana_deposits` row keyed on **tx signature (idempotent)** — the on-chain
  analogue of today's `coin_purchases`. Duplicate signatures are no-ops.
- Emit a notification (`internal/events`/`webhook`) `deposit.completed`.

**Migration** `0032_solana_deposits.up.sql`:
- `deposit_sessions(public_id, user_id, reference_pubkey UNIQUE, asset,
  amount_expected, status, tx_signature, created_at, expires_at)`
- `solana_deposits(tx_signature PRIMARY KEY, user_public_id, session_id,
  coins, amount_usdc, slot, created_at)` — dedupe / replay protection.

**Ships:** deposit modal → confirmed credits.

---

## 5. Workstream C — Solana withdrawal write-path (credits → USDC)

**Goal:** reuse the entire `payout` request→approve→pay→confirm workflow; only the
final "send money" adapter changes from Stripe to Solana.

- Implement `SolanaTransferrer` satisfying `payout.Transferrer`:
  `Transfer(ctx, destWalletAddr, amount, idemKey)` builds an SPL USDC transfer
  from the platform **hot wallet** (key via `secretbox`) to the user's wallet,
  signs, broadcasts, returns the **tx signature** as `transferID`. `PayoutsEnabled`
  → always true for a valid Solana address (no KYC gate in Beta), with basic
  address validation. Idempotency: derive the transfer deterministically / guard on
  `idemKey` to prevent double-send on retry.
- Extend the `payout` state machine to the spec's statuses:
  `requested→approved→processing→broadcasted→confirmed→completed` (+ rejected/failed).
  Add a **confirmation watcher** (worker) that reads `blockchain.Confirmations(sig)`
  and transitions `broadcasted→confirmed→completed`; on chain failure →
  `failed` + `Release` escrow (existing `Bank.Release`).
- Withdrawal destination = user's connected wallet address (from Privy profile) or
  an explicitly entered address; validate on-chain format.
- Keep escrow hold, net-winnings-only, anti-fraud + debt gates, audit — all already
  in `payout`.

**Migration** `0033_solana_withdrawals.up.sql`: extend `withdrawals` with
`dest_wallet_address TEXT`, `chain TEXT DEFAULT 'solana'`, `tx_signature TEXT`;
relax the `status` CHECK to include the new states. (No new table — reuse.)

**Config/secrets:** `SOLANA_HOT_WALLET_SECRET` (secretbox-encrypted), balance
alerting threshold.

**Ships:** withdraw to wallet, auto-confirmed.

---

## 6. Workstream D — Super Admin wallet controls

Wire the spec's controls into the **existing** `internal/adminapi` (Ed25519
platform-authz already enforced at the router):
- **Settings:** supported assets, deposit/withdrawal enabled, min deposit, min/max
  withdrawal, withdrawal fee, confirmation count, maintenance mode.
- **Risk:** freeze/unfreeze wallet, lock withdrawals, force refund, manual
  adjustment — most map to existing `payout` admin actions + a wallet `status` flag.

**Migration** `0034_wallet_settings.up.sql`: a small `wallet_settings` singleton
table (or reuse `platformcfg`). Read-through cached; `wallet.status` per user for
freeze.

---

## 7. Workstream E — Frontend wallet UX

- Add `@solana/web3.js`, `@solana/spl-token`, `@solana/pay` (+ wallet-adapter is
  handled by Privy). Data still comes from **our** API — balances are
  server-authoritative; the frontend never computes balances.
- **Wallet page** (`app/wallet`): connected wallet, available/pending/withdrawable
  balances, deposit + withdraw buttons, tx history (from ledger read API).
- **Deposit modal:** select amount → `POST /v1/deposits` → get reference + ATA →
  build Solana Pay transfer (embedded wallet signs, or external wallet), show QR +
  waiting animation → poll session status → "credited".
- **Withdraw modal:** amount + destination (default connected wallet) + estimated
  fee → existing payout request endpoint → poll status.
- **Notifications:** deposit/withdrawal completed/failed, match won/lost (events
  module).

---

## 8. Cross-cutting: security & correctness

- Server-authoritative balances (already) — never trust the client.
- Idempotency + replay protection keyed on **tx signature** for deposits and on
  `idemKey`/withdrawal id for payouts (patterns already used for Stripe).
- Confirmations threshold before crediting; configurable per asset.
- Immutable ledger is the source of truth; reconciliation job compares on-chain
  deposits/withdrawals vs ledger (extend `payments/reconcile.go`, `ledger/reconcile.go`).
- Hot-wallet key in `secretbox`; low-balance alerting; withdrawal approval
  threshold + daily limits + cooldown (config in Workstream D).
- Listener/worker runs guarded (recover on panic — the repo already added
  goroutine recovery) and independently of the request path.
- Audit logs on every admin money action (`payout.Audit` exists).

---

## 9. New dependencies

- Go: `github.com/gagliardetto/solana-go` (RPC + SPL), a JWKS helper for Privy
  (`github.com/MicahParks/keyfunc` or hand-rolled).
- JS: `@privy-io/react-auth`, `@solana/web3.js`, `@solana/spl-token`, `@solana/pay`.

---

## 10. Phasing & milestones

| Phase | Deliverable | Depends on |
|---|---|---|
| P0 | Decisions signed off; deps added; config/secrets scaffolded; devnet RPC | — |
| P1 | Privy auth end-to-end (backend verifier + `/v1/auth/privy` + mig 0031; frontend PrivyProvider + login) | P0 |
| P2 | Solana deposit read-path (`blockchain` + `solanadeposit` + listener + mig 0032; credit via Coiner) | P1 |
| P3 | Solana withdrawal write-path (`SolanaTransferrer` + payout state-machine extension + watcher + mig 0033) | P2 |
| P4 | Admin wallet settings + risk controls (mig 0034) | P2/P3 |
| P5 | Frontend wallet/deposit/withdraw modals + notifications + tx history | P2/P3 |
| P6 | Security review, reconciliation job, devnet→mainnet cutover | all |

**Verification:** each phase testable on **devnet** with faucet USDC before mainnet.

---

## 11. Explicitly out of scope for Beta (per spec)

KYC, fiat/Stripe/cards/Apple/Google Pay (Stripe kept but disabled), multi-chain,
NFTs, multi-wallet management. USDT deferred (add as a second asset row later).
