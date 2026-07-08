# Wallet pipeline — security review & devnet→mainnet cutover runbook

Covers the Beta wallet pipeline: Privy auth (P1), Solana USDC deposits (P2),
Solana USDC withdrawals (P3), Super Admin controls (P4), notifications (P5), and
reconciliation (P6). This is a **self-audit + operational runbook**, not a
substitute for an external security review before handling real funds.

---

## 1. Security review (self-audit)

### Auth (Privy)
- Privy access token verified with **ES256** against the app verification key,
  with `iss=privy.io`, `aud=<app id>`, and expiry enforced. Algorithm-confusion
  (HS256 downgrade) is rejected — covered by `auth/privy_test.go`.
- The proven claim is only the **Privy user id**. Email / wallet / socials in the
  login `profile` are **non-authoritative hints**; the session is our own HS256
  JWT. ✅
- Disabled cleanly when unconfigured (503), so a misconfig can't open an
  unauthenticated path.

### Deposits
- Credited **only** on a `finalized` on-chain tx; underpayments are ignored.
- **Idempotent** two ways: the ledger key `solana:<sig>` and the `solana_deposits`
  primary key (tx signature) — a re-observed transfer can't double-credit.
- Verifies **mint == USDC** and destination is the **platform ATA** (by ATA or
  owner) via `pre/postTokenBalances`. ✅
- **Credit-before-record** ordering: a crash between the two is safe (ledger key
  makes re-credit a no-op; record insert is idempotent).
- Reference key is 32 bytes of CSPRNG → unpredictable; server-authoritative
  balances throughout (client never asserts balance).

### Withdrawals
- **Net-winnings-only** (deposited/bonus credits are play-only) — kills the
  buy→withdraw laundering vector.
- Escrow **hold** at request; **admin approval + clearing window + anti-fraud +
  outstanding-debt** gates before payout.
- Solana: **atomic `processing` claim before broadcast** prevents a
  double-broadcast / double-spend (Solana sends aren't key-idempotent). Coins are
  **burned only after `finalized` confirmation**; **released** on on-chain
  failure. ✅
- Hot-wallet secret is process env only and **never logged**.

### Admin controls
- Every route is `RequirePlatformOrAdmin` (Ed25519 Platform token **or**
  `ADMIN_USER_IDS`). Every mutation is written to `audit_log`.
- The deposit/withdrawal **gate fails closed** on a settings/DB read error.

### Reconciliation
- Hourly read-only cross-check of on-chain records vs the ledger; **alerts, never
  auto-corrects** (drift ⇒ investigate). Metrics: `wallet_recon_*`.

### Residual risks / MUST-DO before mainnet
1. **Hot-wallet key management.** Currently a plaintext env secret. Before
   mainnet: hold it in a KMS / `secretbox`-at-rest, cap the hot-wallet float,
   **sweep deposits to cold storage**, and alert on low SOL (fees) + high balance.
2. **No per-route rate limiting** on `POST /v1/deposits` and `/v1/withdrawals`.
   Add per-user rate limits (the codebase has `middleware.RateLimit`) to blunt
   abuse/spam session creation.
3. **Withdrawal destination = Privy-profile wallet hint.** Before a user's first
   withdrawal on mainnet, **verify wallet ownership** (a signed message) rather
   than trusting the stored hint.
4. **Broadcast→status-write crash window** leaves a withdrawal in `processing`
   with funds possibly in flight (no double-spend — the claim guards it). The
   reconciler flags `stuck_processing`; recover manually by matching the tx
   on-chain. Consider durable-nonce transactions for exactly-once broadcast.
5. **RPC provider trust.** Use a reputable RPC (Helius/Triton/QuickNode);
   consider dual-RPC confirmation for high-value withdrawals. Prefer a webhook/
   indexer over polling at scale.
6. **Confirmations:** deposits + withdrawals use `finalized` (strongest) — good;
   keep it there for money movement.

---

## 2. Configuration reference

| Env | Purpose | Beta default |
|---|---|---|
| `PRIVY_APP_ID` / `PRIVY_VERIFICATION_KEY` | Privy auth (both or neither) | unset ⇒ Privy login off |
| `NEXT_PUBLIC_PRIVY_APP_ID` (frontend) | Privy provider app id | unset ⇒ CTA hidden |
| `SOLANA_RPC_URL` | Solana JSON-RPC endpoint | unset ⇒ deposits off |
| `SOLANA_COMMITMENT` | commitment level | `finalized` |
| `SOLANA_USDC_MINT` | accepted mint | mainnet USDC |
| `SOLANA_PLATFORM_OWNER` | Solana Pay recipient wallet | — |
| `SOLANA_PLATFORM_ATA` | platform USDC token account (deposits land here) | — |
| `DEPOSIT_SESSION_TTL` / `DEPOSIT_MIN_USDC` / `DEPOSIT_POLL_INTERVAL` | deposit tunables | 30m / 1 / 15s |
| `SOLANA_HOT_WALLET_SECRET` | base58 payout signer (enables Solana withdrawals) | unset ⇒ Stripe/Dev rail |
| `WITHDRAW_CONFIRM_INTERVAL` | confirmation watcher cadence | 15s |
| `WALLET_RECON_INTERVAL` | reconciliation cadence | 1h |
| `PLATFORM_ADMIN_PUBLIC_KEY` | verifies admin Platform tokens | — |
| `ADMIN_USER_IDS` | admin allowlist (comma-sep) | — |

Deposits require RPC + mint + owner + ATA (`Config.DepositsEnabled`). Solana
withdrawals additionally require the hot-wallet secret (`Config.WithdrawalsSolana`).
Coin peg is derived from `COIN_CENTS` (1 USDC = 100/COIN_CENTS credits).

---

## 3. devnet → mainnet cutover

1. **Provision custody:** create the platform Solana wallet + its **USDC ATA**;
   fund the wallet with SOL for tx fees. Store the key in KMS/secretbox.
2. **RPC:** point `SOLANA_RPC_URL` at a mainnet provider; keep `finalized`.
3. **Mint:** set `SOLANA_USDC_MINT` to mainnet USDC
   (`EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v`).
4. **Privy:** switch to the production Privy app id + verification key on both
   backend and frontend.
5. **Admin:** set `PLATFORM_ADMIN_PUBLIC_KEY` (and/or `ADMIN_USER_IDS`).
6. **Migrate:** run migrations (auto-migrate applies 0031–0034).
7. **Gate closed first:** via `PUT /v1/admin/wallet/settings`, start with
   `deposits_enabled=false, withdrawals_enabled=false`.
8. **Smoke test with tiny amounts:** enable deposits, do a $1 deposit end-to-end
   (session → transfer → credited); enable withdrawals, cash out a small amount
   (request → approve → broadcast → confirm → paid).
9. **Watch reconciliation:** confirm `wallet_recon_*` gauges are 0 and no DRIFT
   logs before opening to users.
10. **Open up:** flip the settings switches on; monitor the confirmation watcher
    + reconciler.

**Rollback:** set `maintenance_mode=true` (pauses deposits + withdrawals
instantly) or flip the individual switches; freeze specific wallets as needed.
