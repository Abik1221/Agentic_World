# Beta Readiness — what's left to configure, test (devnet), and implement

_Last updated 2026-07-10. Scope: `backend/` (Go arena). Companion to
`BACKEND_AUDIT_AND_REALTIME_PLAN.md` (audit + fixes) and `GAME_STAKE_TIERS_PLAN.md`._

This is the single checklist to take the backend from "built + unit-green" to
"beta on devnet." It assumes the work already committed on
`feat/agent-manifest-and-beta-loop` (audit fixes; the stake-tier + ranked money
loop; wallet-ownership + hot-wallet-key-at-rest). Baseline: `go build`, `go vet`,
`gofmt`, and the full unit suite are green.

---

## 0. Done (no action needed)

- **Audit fixes:** W1 (Privy takeover), C1 (SSE/long-poll write-timeout), M1
  (refund clawback), M3 (Solana double-pay), G1 (held-Mafia mis-pay), M4/M5/M6,
  R1 (worker panic recovery), R2 (Mafia long-poll), R4 (SSE cap), G2 (Goofspiel +
  Mafia lockless timeout), SEC-M1/M2/M3, P2-SSRF, W8/SEC-L1, **W2** (wallet
  ownership), **W3 key-at-rest**, R3 (pool default 50).
- **Money loop:** admin stake tiers (all games) → tier enforcement → tier-driven
  `/v1/queue` → live-vs-live socket auto-driver (+ 2-live-agent test) → `pyyol
  queue` → ranked docs.

---

## 1. Configuration to set before beta (env)

Set these on the beta server (devnet values first). `config.validate()` fails the
boot if a prod/staging required var is missing.

### Identity / auth
- `PRIVY_APP_ID`, `PRIVY_VERIFICATION_KEY` — from the Privy dashboard (both or neither).
- `JWT_SIGNING_KEY` (≥32 bytes, not a `dev-only-*` placeholder), `API_KEY_PEPPER`.
- `HCAPTCHA_SECRET` — required in prod/staging.
- `CORS_ALLOWED_ORIGINS` — the dashboard/client origins (no `*` in prod/staging).
- `TRUSTED_PROXY_COUNT` — number of proxies in front (default 1).

### Platform config bus (Super Admin ↔ arena)
- `PLATFORM_ADMIN_PUBLIC_KEY` — required in prod/staging (verifies admin config + Platform tokens).
- `PLATFORM_ENGINE_PRIVATE_KEY` — required in prod/staging (signs the admin event stream). Generate both with `go run ./cmd/platform-bus-keygen`.
- `ADMIN_USER_IDS` — user public ids allowed on `/v1/admin/*` (in addition to Platform tokens).

### Solana deposits (devnet first)
- `SOLANA_RPC_URL` (devnet RPC), `SOLANA_COMMITMENT` (finalized), `SOLANA_USDC_MINT` (devnet USDC),
  `SOLANA_PLATFORM_OWNER`, `SOLANA_PLATFORM_ATA` (deposits must land here).
- `DEPOSIT_MIN_USDC`, `DEPOSIT_SESSION_TTL`, `DEPOSIT_POLL_INTERVAL` (defaults fine).

### Solana withdrawals (devnet first)
- **Preferred:** `SOLANA_HOT_WALLET_SECRET_ENC` (base64 secretbox — produce with
  `SOLANA_HOT_WALLET_ENC_KEY=<master> go run ./cmd/wallet-secret-encrypt <base58-key>`)
  **+** `SOLANA_HOT_WALLET_ENC_KEY`. Plaintext `SOLANA_HOT_WALLET_SECRET` is dev-only
  (warned in prod).
- `WITHDRAW_CONFIRM_INTERVAL`, `WALLET_RECON_INTERVAL` (defaults fine).

### Economy / scale / features
- `COIN_CENTS` — must divide 100 exactly (default 1 = 1 coin/¢).
- `DB_MAX_CONNS` — default 50; tune vs Postgres `max_connections` ÷ instance count.
- `SSE_MAX_CONNS` — instance-wide spectator cap (default 20000).
- `RANKED_AUTODRIVE` — **leave `false` until devnet-tested (§3)**; then flip to `true`.
- `STRIPE_SECRET_KEY` / `STRIPE_WEBHOOK_SECRET` — only if the fiat rail is used (else the Solana rail + DevGateway).

> Full env table lives in `backend/docs/runbooks/wallet-pipeline.md`.

---

## 2. Test on devnet (end-to-end checklist)

Boot the server against **devnet RPC + a disposable Postgres + Redis**. Fund a
devnet hot wallet with SOL + USDC and a test user wallet.

### Wallet on/off-ramp
- [ ] **Deposit:** create a deposit session (`POST /v1/deposits`), send devnet USDC
      to the platform ATA with the Solana Pay reference → session flips
      `pending→detected→completed`, coins credited exactly once (idempotent on tx sig).
- [ ] **Underpay / wrong-mint / wrong-destination** → NOT credited.
- [ ] **Wallet-ownership verify (W2):** `POST /v1/wallet/verify/challenge` →
      sign the returned message with the wallet key → `POST /v1/wallet/verify` →
      `users.verified_wallet_address` set.
- [ ] **Withdraw:** with a verified wallet + net winnings, request → admin approve →
      broadcast → confirm-watcher marks `paid` on finalize; coins burn only then.
      With an **unverified** wallet → `wallet_not_verified` (W2). Ambiguous broadcast
      → stays `broadcasted`, never double-pays (M3).
- [ ] **Hot-wallet key encrypted (W3):** boot with `SOLANA_HOT_WALLET_SECRET_ENC` +
      `SOLANA_HOT_WALLET_ENC_KEY` (no plaintext) → withdrawals sign correctly.
- [ ] **Reconciliation:** `walletrecon` runs, flags no false drift.

### The ranked money loop (agents vs agents)
- [ ] **Stake tiers:** admin `PUT /v1/admin/games/mafia/stakes`; `GET /v1/games/mafia/stakes` (public) reflects it within ~10s.
- [ ] **Two live agents:** `pyyol run` two agents → each `pyyol queue --game goofspiel --tier mid`
      → paired → **with `RANKED_AUTODRIVE=true`**, the server drives both sockets to a
      settled finish; winner/loser coins move, rake taken.
- [ ] **Budget gates:** a tier above an agent's `max_bid` → rejected; a broke agent → stuck `waiting` (known P5).
- [ ] **Certification gate:** an uncertified agent → `/v1/queue` rejects (run `pyyol publish` first).

### Admin surface (Platform Ed25519 token)
- [ ] Stake-tier GET/PUT (+ validation rejects bad input), wallet settings/freeze/adjust, withdrawal approve/reject.
      Mint via `PLATFORM_ADMIN_PRIVATE_KEY=<seed> go run ./cmd/platform-token`.

### Resilience
- [ ] **Redis down:** matches still time out + settle (lockless-OCC: Goofspiel + Mafia); long-poll degrades to the 2s backstop, not a hang.
- [ ] **Worker panic:** a forced panic in one background loop is recovered + the loop restarts (SafeLoop); process stays up.

---

## 3. Flags to flip once devnet-verified

- `RANKED_AUTODRIVE=true` — turns on hands-free live-vs-live driving of paired
  matches. Off by default because it auto-plays **real staked** matches; flip only
  after the two-live-agent devnet test above passes.

---

## 4. Still to implement — backend

| Item | What | Why it's not done |
|---|---|---|
| **W3 float-cap + cold sweep + low-balance alert** | Cap hot-wallet balance, sweep excess to cold storage, gauge + alert on low balance | Needs a **cold-storage wallet address / infra decision**. (Key-at-rest is done.) |
| **R3 request/worker DB-pool split** | Separate pools so a worker/settlement burst can't starve request traffic | Services are shared between handlers + workers; a real refactor that wants **load testing** to size. Default raised to 50 as the interim. |
| **Mafia lockless `Act`** | State-versioning OCC so `Act` can go lockless (not just the timeout path) | No-event night submissions aren't seq-protected; needs a `matches.version` optimistic column. Money-stuck case already fixed via the lockless timeout (G2/G3). |
| **MonopolyWallet** | Wire a real wallet adapter so Monopoly stakes actually move (settlement is latent today, G4) | Product decision on Monopoly economics; tier plumbing is already in place. |
| **12-seat Mafia ranked driver** | A Mafia-shaped multi-seat variant of the live-vs-live driver | Goofspiel (2-player) is done; Mafia needs enough queued agents at a tier + a multi-seat drive loop (reuse `mafia/pushplay`). |
| **P4 finish** | `/v1/queue` currently matchmakes Goofspiel only (game param accepted) | Broad matchmaking for Mafia/Monopoly follows the driver work above. |
| **Full BASE_URL integration test** | server+PG+Redis+`/v1/queue`+2 WS agents+settle in CI | The in-process 2-agent test covers the drive mechanism; this confirms real-server wiring. Wire into `.github/workflows/backend-e2e.yml`. |

---

## 5. Frontend / other repos (not this backend)

- **Wallet-ownership sign flow:** call the connected wallet's `signMessage(message)`
  (Privy/Phantom/…), base58-encode the signature, `POST /v1/wallet/verify`.
- **`pyyol queue` for JS:** the CLI is Python-only; JS agents use `agent.run()` in
  code and have no queue command yet.
- **Super Admin UI:** consume the stake-tier + wallet-admin APIs (separate admin repo).
- **Deposit/withdraw modals** already exist; wire the verify step before the first withdrawal.

---

## 6. Ops / infra decisions

- **KMS (deferred):** the hot-wallet key is `secretbox`-encrypted at rest with an
  env master key. Moving the master key to a managed KMS (AWS/GCP) is the stronger
  posture for mainnet — your call.
- **Cold storage:** a cold wallet address for the float sweep (unblocks the rest of W3).
- **Prod secrets:** real (non-placeholder) JWT key, API key pepper, platform bus
  keypair, Stripe webhook secret (if fiat). Never in the image; `.env` is gitignored.
- **Postgres sizing:** `max_connections` ≥ `DB_MAX_CONNS × instances` + headroom.
- **Monitoring:** expose the existing gauges (`sse_subscribers`, `sse_dropped_slow_total`,
  `wallet_recon_*`, webhook health) on dashboards; alert on hot-wallet low balance once §4 lands.
- **devnet → mainnet cutover:** see `backend/docs/runbooks/wallet-pipeline.md §3`.
