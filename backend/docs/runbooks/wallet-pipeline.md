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
- **Wallet-ownership proof (W2):** the payout destination must be a wallet the user
  has *proven* they control — `POST /v1/wallet/verify/challenge` → sign the nonce →
  `POST /v1/wallet/verify` (Ed25519). `Request` returns `wallet_not_verified` unless
  the verified wallet matches the linked destination. ✅
- **Hot-wallet key at rest (W3):** prefer `SOLANA_HOT_WALLET_SECRET_ENC` (secretbox
  AES-256-GCM, decrypted at boot with `SOLANA_HOT_WALLET_ENC_KEY`); plaintext
  `SOLANA_HOT_WALLET_SECRET` is dev-only and warned in prod. Never logged. ✅
- Per-user **rate limits** on `POST /v1/withdrawals` (and `/v1/deposits`). ✅

### Admin controls
- Every route is `RequirePlatformOrAdmin` (Ed25519 Platform token **or**
  `ADMIN_USER_IDS`). Every mutation is written to `audit_log`.
- The deposit/withdrawal **gate fails closed** on a settings/DB read error.

### Reconciliation
- Hourly read-only cross-check of on-chain records vs the ledger; **alerts, never
  auto-corrects** (drift ⇒ investigate). Metrics: `wallet_recon_*`.

### Residual risks / MUST-DO before mainnet
1. **Hot-wallet float + cold storage (still open).** Key-at-rest is done (W3,
   `secretbox`); still needed for mainnet: **cap the hot-wallet float, sweep
   excess to cold storage, and alert on low SOL (fees) + high balance.** Needs a
   cold-wallet address (infra decision). Moving the `secretbox` master key into a
   managed KMS is the stronger posture. ✅ key-at-rest / ⬜ float+sweep+alert.
2. ✅ **Per-user rate limiting** on `POST /v1/deposits` and `/v1/withdrawals` (done, SEC-M1).
3. ✅ **Wallet-ownership verification** before withdrawal (done, W2 — see Withdrawals above).
4. **Broadcast→status-write crash window** leaves a withdrawal in `processing`
   with funds possibly in flight (no double-spend — the claim guards it; an
   *ambiguous* broadcast now stays `broadcasted` for the confirm watcher rather
   than double-paying, M3). The reconciler flags `stuck_processing`; recover
   manually by matching the tx on-chain. Consider durable-nonce transactions for
   exactly-once broadcast.
5. **RPC provider trust.** The **free public RPC** (`https://api.mainnet-beta.solana.com`)
   is fine for launch volume — Pyyol only needs read (confirm deposits) + send
   (broadcast withdrawals) on `finalized`; no indexer, webhooks, or on-chain
   programs. If you hit public-RPC rate limits at scale, swap `SOLANA_RPC_URL` to a
   paid provider (no code change). Consider dual-RPC confirmation only for
   high-value withdrawals.
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
| `SOLANA_CLUSTER` | **`devnet` or `mainnet-beta`** — the network switch; cross-checked against the RPC URL and the mint | — (required) |
| `SOLANA_PLATFORM_ATA` | platform USDC token account (deposits land here) | — |
| `HOT_WALLET_CAP_CENTS` | most we accept sitting in the hot wallet before the monitor asks for a cold sweep; `0` disables | `0` |
| `DEPOSIT_SESSION_TTL` / `DEPOSIT_MIN_USDC` / `DEPOSIT_POLL_INTERVAL` | deposit tunables | 30m / 1 / 15s |
| `SOLANA_HOT_WALLET_SECRET` | base58 payout signer, PLAINTEXT (dev/local; warned in prod) | unset ⇒ Stripe/Dev rail |
| `SOLANA_HOT_WALLET_SECRET_ENC` | **preferred (prod):** base64(secretbox) of the signer, decrypted at boot; takes precedence over the plaintext form. Produce with `go run ./cmd/wallet-secret-encrypt` | — |
| `SOLANA_HOT_WALLET_ENC_KEY` | master key that decrypts `SECRET_ENC` (never logged; required when `SECRET_ENC` is set) | — |
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
2. **RPC:** point `SOLANA_RPC_URL` at mainnet (the free public
   `https://api.mainnet-beta.solana.com` is fine to start); keep `finalized`.
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

---

## 4. devnet → mainnet: the exact GitHub Secrets to flip (`Agentic_World` repo)

Deploys read all config from GitHub Secrets, so a mainnet cutover is a **6-secret
swap + redeploy** — no code change. Fund a fresh mainnet wallet first (never reuse
the devnet key), then update these in `Agentic_World → Settings → Secrets`:

| Secret | devnet (testing) | mainnet (production) |
|---|---|---|
| `SOLANA_RPC_URL` | `https://api.devnet.solana.com` | `https://api.mainnet-beta.solana.com` |
| `SOLANA_USDC_MINT` | `4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU` (devnet USDC) | `EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v` (mainnet USDC) |
| `SOLANA_PLATFORM_OWNER` | devnet platform wallet address | **mainnet** platform wallet address |
| `SOLANA_PLATFORM_ATA` | devnet platform USDC ATA | **mainnet** platform USDC ATA |
| `SOLANA_HOT_WALLET_SECRET_ENC` | `secretbox(devnet payout key)` | `secretbox(mainnet payout key)` |
| `SOLANA_HOT_WALLET_ENC_KEY` | devnet master key | **new** mainnet master key |

`SOLANA_COMMITMENT` stays `finalized`. `PRIVY_*` switch to the production Privy app.
Produce each `*_ENC` value locally with `cd backend && go run ./cmd/wallet-secret-encrypt`
(paste the base58 key, keep the emitted `ENC` + `ENC_KEY`; never commit the raw key).

**Fiat is optional.** Leave `STRIPE_SECRET_KEY` unset to run Solana-only: the arena
boots, card top-ups return 503 (never credited free), and USDC deposits are the sole
on-ramp. Only set the full `STRIPE_*` set if you later add card top-ups.

After swapping, redeploy (push or `workflow_dispatch`), keep the wallet gate closed
(`deposits_enabled=false`), run the tiny-amount smoke test in §3.8, then open up.


## Hot-wallet exposure

The hot wallet holds a live signing key, so **its balance is the maximum a key
compromise can take**. Keep only the working capital outstanding payouts need there
and hold the rest at a cold address this server has no key for.

Set `HOT_WALLET_CAP_CENTS` to that working-capital ceiling. The solvency monitor
already reads the on-chain balance each pass; above the cap it exports
`treasury_excess_exposure_cents` and logs `HOT WALLET OVER EXPOSURE CAP`. Alert on
that gauge being `> 0` for more than one interval.

Sizing: cover a normal day of payouts with headroom — roughly
`p95 daily payout volume x 2`. Too low and you page yourself over routine float; too
high and the cap defends nothing.

**The sweep is deliberately manual.** Moving the excess out automatically would need a
second signing key in this same process, which would recreate on the sweep path the
exact exposure the cap exists to limit. The alert is the signal for a human to sweep
to cold storage.

Leave it at `0` on devnet — the tokens are worthless there and the alert is only noise.


## Switching between devnet and mainnet

`SOLANA_CLUSTER` is the single switch. Set it as a GitHub secret to `devnet` or
`mainnet-beta`.

It is a named cluster rather than a `DEVNET=true` boolean for two reasons. A boolean
has no safe default — unset meaning mainnet risks a misconfigured deploy touching real
funds; unset meaning devnet risks a mainnet deploy silently running fake. And a boolean
cannot be cross-checked, which is the part that actually matters: **the danger was never
which flag you set, it is a mismatch between the flag, the RPC URL, and the mint.**

`SOLANA_USDC_MINT` defaults to the **real mainnet USDC mint**. A devnet deploy that
forgets to set it settles devnet play in real USDC, against a devnet RPC, with nothing
in the logs saying anything is wrong. That is the failure this refuses to boot on.

The backend will not start if any pair disagrees:

| check | refused when |
|---|---|
| cluster vs RPC | `mainnet-beta` with a devnet/testnet/localhost RPC |
| cluster vs RPC | `devnet` with an RPC that doesn't look like devnet (it may be mainnet — real funds) |
| cluster vs mint | `devnet` with the mainnet USDC/USDT mint (i.e. the default, left unset) |
| cluster vs mint | `mainnet-beta` with an unrecognised mint (would credit a worthless token) |
| mainnet only | withdrawals enabled with `HOT_WALLET_CAP_CENTS` unset or `0` |
| mainnet only | `ALLOW_MINT` on (free coins with no deposit behind them) |

A process that will not boot gets fixed in minutes. A process that silently mixes test
and real money is not noticed until the money is gone.

### Beta (now)

```
SOLANA_CLUSTER      = devnet
SOLANA_RPC_URL      = https://api.devnet.solana.com
SOLANA_USDC_MINT    = <your devnet mint>       # must NOT be the mainnet default
HOT_WALLET_CAP_CENTS= 0                        # off; devnet tokens are worthless
```

### Going to mainnet

Change `SOLANA_CLUSTER` to `mainnet-beta` and, in the same change, the RPC URL, the
mint, the platform owner/ATA, and the hot-wallet secret. Set `HOT_WALLET_CAP_CENTS`
to a real ceiling — boot refuses without one. If you miss any of these the deployment
fails loudly rather than half-switching.
