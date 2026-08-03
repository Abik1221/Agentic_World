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
| `SOLANA_CLUSTER` | **`devnet` or `mainnet-beta`** — the network switch. Derives the three values below, and is cross-checked against them | — (required) |
| `SOLANA_RPC_URL` | JSON-RPC endpoint. **Derived from the cluster**; set only to override (private/paid node) | per cluster |
| `SOLANA_USDC_MINT` | accepted mint. **Derived from the cluster**; set only to override (other stablecoin, own devnet token) | per cluster |
| `SOLANA_EXPLORER_TX` | receipt link template. **Derived from the cluster** (devnet gets `?cluster=devnet`) | per cluster |
| `SOLANA_COMMITMENT` | commitment level | `finalized` |
| `SOLANA_PLATFORM_OWNER` | Solana Pay recipient wallet | — |
| `SOLANA_PLATFORM_ATA` | platform USDC token account (deposits land here) | — |
| `SOLANA_PAYOUT_ATA` | token account payouts are signed FROM. Unset ⇒ same as the deposit account (single wallet). Set ⇒ custody is split | unset |
| `SOLANA_COLD_WALLET_ADDRESS` | sweep destination, named in the exposure alert. Never signed for here | — (required on mainnet) |
| `HOT_WALLET_CAP_CENTS` | most we accept sitting in the hot wallet before the monitor asks for a cold sweep; `0` disables | `0` |
| `HOT_WALLET_MIN_SOL_LAMPORTS` | native-SOL floor on the signing wallet; `0` disables | `0` (required `>0` on mainnet) |
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

Deploys read all config from GitHub Secrets, so a mainnet cutover is a **secret swap +
redeploy** — no code change.

**The network constants are derived from `SOLANA_CLUSTER` and no longer need setting.**
The USDC mint, the default RPC endpoint and the explorer URL template are public, fixed
properties of a network, so the cluster name determines them (`config.go`,
`solanaClusterDefaults`). Hand-pasting them was how they drifted — and the specific
failure it caused is worth remembering: `SOLANA_USDC_MINT` used to default to the REAL
mainnet mint on *every* cluster, so a devnet deploy that forgot it settled devnet play
against real USDC while pointing at a devnet RPC, with nothing in the logs saying so.
That cannot happen to a value nobody has to remember to set.

Set only what is genuinely specific to this deployment. Fund a fresh mainnet wallet
first (never reuse the devnet key), then update these in
`Agentic_World → Settings → Secrets`:

| Secret | devnet (testing) | mainnet (production) |
|---|---|---|
| `SOLANA_CLUSTER` | `devnet` | `mainnet-beta` |
| `SOLANA_PLATFORM_OWNER` | devnet platform wallet address | **mainnet** platform wallet address |
| `SOLANA_PLATFORM_ATA` | devnet platform USDC ATA | **mainnet** platform USDC ATA |
| `SOLANA_HOT_WALLET_SECRET_ENC` | `secretbox(devnet payout key)` | `secretbox(mainnet payout key)` |
| `SOLANA_HOT_WALLET_ENC_KEY` | devnet master key | **new** mainnet master key |
| `HOT_WALLET_CAP_CENTS` | `0` (off) | real ceiling — **boot refuses without one** |
| `HOT_WALLET_MIN_SOL_LAMPORTS` | `0` (off) | e.g. `50000000` (0.05 SOL) — **boot refuses without one** |
| `SOLANA_COLD_WALLET_ADDRESS` | optional | offline address — **boot refuses without one** |
| `SOLANA_PAYOUT_ATA` | unset | set to split custody (see §5) |

Optional overrides, same names as always, for when the derived value is not what you
want — a paid/private RPC (Helius, QuickNode, self-hosted), a different accepted
stablecoin, your own devnet test token, or a different explorer:
`SOLANA_RPC_URL`, `SOLANA_USDC_MINT`, `SOLANA_EXPLORER_TX`. The cross-checks in §"Switching
between devnet and mainnet" still police these, so a wrong override fails the boot rather
than settling quietly.

`SOLANA_COMMITMENT` stays `finalized`. Produce each `*_ENC` value locally with
`cd backend && go run ./cmd/wallet-secret-encrypt` (paste the base58 key, keep the emitted
`ENC` + `ENC_KEY`; never commit the raw key).

**Privy is not part of this.** The frontend has no Privy dependency and no Privy code —
login is email/password + magic-link, and wallet auth uses the injected provider. The
backend verifier still exists and is disabled when `PRIVY_APP_ID` /
`PRIVY_VERIFICATION_KEY` are unset (`/v1/auth/privy` answers 503). Nothing to switch.

### The frontend RPC is a SEPARATE repo and is baked at BUILD time

`NEXT_PUBLIC_SOLANA_RPC_URL` lives in **`Pyyol_client`** secrets and is compiled into the
image (`deploy-landing.yml`, `--build-arg`). It is not derived, because the browser never
reads the backend's env.

So a cutover needs **both** repos redeployed. Miss it and every deposit is built against
the wrong network while the listener watches the right one: nothing is stolen — the wallet
rejects a foreign blockhash — but every deposit fails with an opaque wallet error.

This is now caught rather than merely documented: the backend publishes `solana_cluster`
in `GET /v1/config`, and the client compares it against its own RPC's **genesis hash**
before signing or rendering a QR (`lib/solana-pay.ts`, `assertClusterAgrees`). A mismatch
shows a stated misconfiguration instead of a wallet error. The check uses the genesis hash
rather than the URL because a private endpoint's URL says nothing about its network.

**Fiat is optional.** Leave `STRIPE_SECRET_KEY` unset to run Solana-only: the arena
boots, card top-ups return 503 (never credited free), and USDC deposits are the sole
on-ramp. Only set the full `STRIPE_*` set if you later add card top-ups.

After swapping, redeploy (push or `workflow_dispatch`), keep the wallet gate closed
(`deposits_enabled=false`), run the tiny-amount smoke test in §3.8, then open up.


## 5. Custody: cold / vault / hot

The hot wallet holds a live signing key, so **its balance is the maximum a key
compromise can take**. Keep only the working capital outstanding payouts need there
and hold the rest at a cold address this server has no key for.

### The three tiers

| Tier | What it is | Key lives | Config |
|---|---|---|---|
| **Cold** | the reserve; holds the bulk | offline (hardware / multisig), never on a server | `SOLANA_COLD_WALLET_ADDRESS` (address only) |
| **Vault** | where deposits land and accumulate | offline — the server watches it, cannot sign for it | `SOLANA_PLATFORM_ATA` + `SOLANA_PLATFORM_OWNER` |
| **Hot** | small float; signs payouts only | `secretbox` on the server | `SOLANA_PAYOUT_ATA` + `SOLANA_HOT_WALLET_SECRET_ENC` |

**Leave `SOLANA_PAYOUT_ATA` unset and you have one wallet doing both jobs** — the
original setup, unchanged, and still the default. Deposits land in the same account
payouts are signed from, so 100% of user funds sit under a key on the app server, and
`HOT_WALLET_CAP_CENTS` gets breached by ordinary deposit volume. That last part is the
real cost: an alert that fires during normal business teaches you to ignore the one
control that bounds blast radius.

**Setting `SOLANA_PAYOUT_ATA` to a different account splits custody.** Deposits keep
arriving in the vault; payouts draw on the float. Boot logs `custody_split=true`.

Why this is affordable *here*: cash-outs already wait on admin approval and a 24h
clearing window, so a human is in the loop regardless and a manual cold→hot top-up adds
no user-visible latency. An exchange promising instant withdrawals could not do this.

### What the monitor reports

Each pass reads the payout account, the vault (when split) and the hot wallet's SOL:

| Gauge | Meaning |
|---|---|
| `treasury_balance_cents` | the float — what can be paid out right now |
| `treasury_vault_balance_cents` | the vault; `0` when custody is not split |
| `treasury_custody_cents` | hot + vault: everything the platform holds |
| `treasury_shortfall_cents` | liability − **custody**; `>0` is a real insolvency |
| `treasury_excess_exposure_cents` | float above the cap; `>0` ⇒ sweep |
| `hot_wallet_sol_lamports` | fee fuel (see below) |

Two conditions a single balance-vs-liability comparison would conflate:

- `TREASURY SHORTFALL` (ERROR) — custody below liability. **The money is not there.**
- `HOT WALLET UNDERFUNDED` (WARN) — custody covers it, the float does not. **Top up
  from cold.** Routine on a split deployment; not an incident.

Alert on `treasury_shortfall_cents > 0` and on `treasury_excess_exposure_cents > 0` for
more than one interval.

Sizing the cap: cover a normal day of payouts with headroom — roughly
`p95 daily payout volume x 2`. Too low and you page yourself over routine float; too
high and the cap defends nothing.

**The sweep is deliberately manual.** Moving the excess out automatically would need a
second signing key in this same process, which would recreate on the sweep path the
exact exposure the cap exists to limit. The alert is the signal for a human to sweep to
`SOLANA_COLD_WALLET_ADDRESS` — which is configured purely so the alert can name the
destination, rather than leaving you to look it up mid-incident, which is when the wrong
address gets pasted.

Leave the cap at `0` on devnet — the tokens are worthless there and the alert is only
noise.

### SOL: the failure with no other symptom

Every payout is a Solana transaction and the hot wallet pays its own fee. A wallet full
of USDC and empty of SOL **cannot pay anybody**, and no USDC-denominated check can see
it coming: solvency is perfect, the breaker is green, every gate is open, and every
cash-out fails at broadcast.

`HOT_WALLET_MIN_SOL_LAMPORTS` is the floor. Below it the monitor logs
`HOT WALLET LOW ON SOL` at ERROR and `hot_wallet_sol_lamports` drops under the
threshold — alert on it. `0` disables the check, which is right on devnet where SOL is
airdropped; **mainnet refuses to boot without one.** A payout that must create the
recipient's token account costs ~0.00204 SOL in rent, so `50000000` (0.05 SOL) is
roughly 24 first-time payouts of headroom.

The boot log line `payout rails verified` also proves the hot wallet actually holds
authority over the account it signs from. `PAYOUT RAILS MISCONFIGURED` means every
cash-out will fail at broadcast — the classic cutover mistake, a fresh key paired with
an ATA from the old deployment.

### What the admin sees at approval

Approval consults the float before building the transfer. If the wallet cannot settle
the cash-out, the approval is **refused** with `treasury_unfunded` and a sentence naming
what to move — USDC or SOL. The withdrawal stays `requested` and is approvable the moment
you top up; nothing is released, nothing is burned, no notification tells the user their
withdrawal failed.

That is the point: without the check the approval succeeds, the broadcast fails, and the
user is told their withdrawal FAILED for an operational shortfall on our side — recorded
against their payout history and needing a manual retry.

The check **fails open** on anything it cannot prove (no reading yet after a restart, or
a reading older than four monitor intervals). A pre-check that halted payouts because its
own RPC was down would turn an observability outage into a money outage.

`GET /v1/admin/overview` carries the same picture for the dashboard: `hot_usdc_cents`,
`vault_usdc_cents`, `custody_split`, `sweep_needed_cents`, `cold_wallet_address`,
`hot_wallet_sol`, `hot_wallet_sol_low`, `payouts_funded_ok` and
`payouts_blocked_reason`.


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
| cluster present | any Solana setting configured with `SOLANA_CLUSTER` unset or invalid |
| cluster vs RPC | `mainnet-beta` with a devnet/testnet/localhost RPC |
| cluster vs RPC | `devnet` with an RPC that doesn't look like devnet (it may be mainnet — real funds) — WARN, not fail |
| cluster vs mint | `devnet` with the mainnet USDC/USDT mint (only reachable now via an explicit override) |
| cluster vs mint | `mainnet-beta` with an unrecognised mint (would credit a worthless token) |
| mainnet only | withdrawals enabled with `HOT_WALLET_CAP_CENTS` unset or `0` |
| mainnet only | withdrawals enabled with `HOT_WALLET_MIN_SOL_LAMPORTS` unset or `0` |
| mainnet only | withdrawals enabled with `SOLANA_COLD_WALLET_ADDRESS` unset |
| mainnet only | `ALLOW_MINT` on (free coins with no deposit behind them) |
| any cluster | `SOLANA_COLD_WALLET_ADDRESS` equal to the platform owner or to either token account — a "cold" address the server can sign for is not cold |

A process that will not boot gets fixed in minutes. A process that silently mixes test
and real money is not noticed until the money is gone.

### Beta (now)

```
SOLANA_CLUSTER       = devnet
SOLANA_PLATFORM_OWNER= <devnet platform wallet>
SOLANA_PLATFORM_ATA  = <devnet platform USDC ATA>
HOT_WALLET_CAP_CENTS = 0     # off; devnet tokens are worthless
HOT_WALLET_MIN_SOL_LAMPORTS = 0     # off; devnet SOL is airdropped
# RPC, mint and explorer come from the cluster. Set SOLANA_USDC_MINT only if you
# deposit your own devnet test token rather than Circle's devnet USDC.
```

### Going to mainnet

Change `SOLANA_CLUSTER` to `mainnet-beta` and, in the same change: the platform
owner/ATA, the hot-wallet secret, `HOT_WALLET_CAP_CENTS`, `HOT_WALLET_MIN_SOL_LAMPORTS`
and `SOLANA_COLD_WALLET_ADDRESS`. Boot refuses without the last three. The RPC, mint and
explorer follow the cluster, so there is nothing to keep in sync by hand.

**Redeploy `Pyyol_client` too**, with `NEXT_PUBLIC_SOLANA_RPC_URL` pointed at mainnet —
it is a separate repo and the value is baked at build time. If you miss it, deposits fail
with a stated cluster mismatch rather than an opaque wallet error, but they still fail.

If you miss anything on the backend the deployment fails loudly rather than
half-switching.


## Agent disconnects during a staked match

A dropped socket must not cost a move. The engine's fallback is correct for an agent
that answers badly or slowly — but an agent mid-reconnect has not answered at all,
and charging it a turn for a network blip is not the same thing. In a staked match a
run of blips could lose the whole stake without the agent ever making a decision.

`AGENT_RECONNECT_GRACE` (default `8s`) is how long a turn waits for the agent to come
back before the fallback is played. It is a bound, not a promise: an agent that is
genuinely gone cannot stall the table beyond it, and a move deadline shorter than the
grace still wins.

Raise it if you see fallback moves attributed to `transport` errors on agents that are
otherwise healthy. Lower it only if tables are visibly stalling — the cost of a longer
grace is latency on a table whose player has already left, and the cost of a shorter
one is somebody's stake.


## Ranked integrity — proving a match was played by an AI

Pyyol is an arena for AI agents and ranked carries real money, so a hand-written
deterministic script taking stakes from developers genuinely paying for inference is
the thing this control exists to stop.

You cannot detect an LLM by looking at moves — timing, entropy and novelty are all
evadable, and all produce false positives that ban legitimate developers. The only
sound basis is proof of work performed: a server-observed model call bound to one
specific decision.

**How it works.** The platform mints a token per turn (`TURN_PROOF_SECRET`) and ships
it in the turn view. The SDK attaches it to the model call as `X-Pyyol-Proof`. The
gateway verifies it and records that decision as bound. At settlement, if a seat's
proven share falls below `RANKED_INTEGRITY_MIN_PCT`, the match is **voided**: both
stakes go back, nobody is paid, and the seat is logged for review.

| Env | Meaning |
| --- | --- |
| `TURN_PROOF_SECRET` | mints the per-turn tokens. Unset ⇒ no proof exists ⇒ nothing to enforce |
| `RANKED_INTEGRITY_MIN_PCT` | required share, **inclusive**. Majority is `51`. `100` is achievable. `0` = off |

### Roll it out in this order

1. **Deploy with `TURN_PROOF_SECRET` set and `RANKED_INTEGRITY_MIN_PCT=0`.** Proofs
   start accumulating; nothing is enforced.
2. **Ship the proof-carrying SDK.** Until developers are running it, no honest agent
   sends a proof and every one of them measures 0%.
3. **Wait, then look at the real distribution.** Batching, caching and a model timing
   out into a deterministic fallback are all legitimate and produce fewer proofs than
   decisions. The right threshold is an observation, not a guess.
4. **Only then set the threshold.**

Enabling this before step 2 would void every ranked match on the platform.

### Why void instead of forfeit

A false positive costs nobody money — the match simply did not happen. Forfeiting the
stake to the opponent would take real funds from a developer on a detection that is
new and imperfect, and that is not recoverable the way an un-played match is.

### Why it fails open

If the proven-decision count cannot be read, the match settles normally. Voiding on a
database hiccup would cancel legitimate matches in bulk during an outage; a cheat that
slips through is still recorded and reviewable afterwards.
