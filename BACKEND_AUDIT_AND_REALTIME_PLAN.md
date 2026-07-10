# Pyyol Backend — Deep End-to-End Audit & Low-Latency Real-Time Plan

_Audit date: 2026-07-10. Scope: `backend/` (Go arena) + `sdk/` (JS/Python) — money/wallet/exchange,
game engines, sandbox & push-play, real-time fabric, SDK, and cross-cutting security._
_Method: 7 parallel domain auditors (Opus) reading against source; every CRITICAL and the
top money/engine HIGHs re-verified by hand at the cited line. Branch `feat/agent-manifest-and-beta-loop`._

---

## 0. Executive summary

The backend is **well-architected and already heavily hardened** (the prior pre-beta remediation
shows: alg-confusion-safe JWT, Ed25519 platform tokens with exp+max-age, balanced double-entry ledger
with `FOR UPDATE` + idempotency keys, SSRF-guarded outbound client, fail-closed prod config, no schema
drift, complete down-migrations). **Baseline is green: `go build`, `go vet`, and the full unit suite all
pass (exit 0).** SDK HMAC signing is **proven byte-identical across Go/JS/Python** (golden `d0da90…`,
verified by executing both suites and re-deriving the Go construction).

The residual risk is **not** in the core money math or the rules engines — both survived adversarial
review. It is concentrated in **five places**:

1. **One exploitable auth bug** (Privy account-takeover) that must block any funded beta.
2. **One real-time show-stopper** (a global write timeout that kills every SSE stream and long-poll).
3. **A cluster of money-edge correctness gaps** (refund clawback, Solana broadcast ambiguity, held
   multi-winner settlement) — each a real loss/double-pay path, all at the ledger↔chain boundary.
4. **Process & streaming robustness for scale** (missing panic recovery on ~15 workers, undersized DB
   pool, no global SSE cap, Mafia long-poll regression).
5. **Assurance gaps** (the "certified" ranked gate never exercises the agent's move logic; hot-wallet
   custody is a plaintext env var; unverified withdrawal destination).

**Nothing here means "rewrite."** These are targeted fixes on a fundamentally sound system. The plan in
§4–§5 sequences them: launch-blockers first (P0), then the real-time hardening that separates
"works in a demo" from "smooth under 10k concurrent spectators."

### Verification status legend
- **✅ Verified** — I re-read the cited code and confirmed the defect.
- **⚠ Reported** — high-confidence agent finding with precise cite; not independently re-read (verify during fix).
- **❌ Corrected** — agent finding that verification showed is already mitigated.

---

## 1. Launch-blocking findings (fix before any funded beta)

| ID | Sev | Title | Location | Impact | Status |
|----|-----|-------|----------|--------|--------|
| **W1** | 🔴 CRITICAL | Privy login links to any pre-existing account via **client-supplied, unverified email** → account takeover | `store/identity_repo.go:565-595` ← `identity/handler.go:200-229` ← `identity/privy.go:45-75` | Attacker with their own valid Privy token + `profile.email=victim@…` takes over any legacy email/password account (all have `privy_user_id IS NULL`) — its balance, withdrawable winnings, wallet controls. `profile.email` is explicitly documented non-authoritative yet used as the join key. | ✅ Verified |
| **C1** | 🔴 CRITICAL | Global `WriteTimeout: 15s` force-closes **every SSE stream and every max-length long-poll** | `config.go:187` → `httpx/server.go:30`; SSE `spectator/handler.go:33-89`, `mafia/handler.go`, `monopoly/handler.go` use `ResponseController` for `Flush()` only, never `SetWriteDeadline` | Go's `WriteTimeout` is an absolute per-connection deadline set once at request start; `Flush` doesn't reset it. Every live "watch match" stream dies at 15s — before the 25s keepalive fires — so spectating degrades into a reconnect-every-15s storm (~667 reconnects/s at 10k viewers, each replaying full history). `state?wait=true` long-polls that run to 15s reset the agent's connection mid-move. **Found independently by 2 auditors.** | ✅ Verified |

---

## 2. High-severity findings (fix in the launch window)

### Money / exchange boundary
| ID | Sev | Title | Location | Impact | Status |
|----|-----|-------|----------|--------|--------|
| **M1** | 🟠 HIGH | Multiple partial refunds on one PaymentIntent claw back only the **first** | `payments/service.go:274-278` | Reversal keyed on a single flat `reversal:{PI}`. $5 then $15 refund on a $20 pack → first claws 600 coins, second (same key) no-ops; 1800 coins ($18) stay spendable/withdrawable. Net platform loss. | ✅ Verified |
| **M3** | 🟠 HIGH | Solana payout **releases escrow on an ambiguous broadcast error → double pay** | `payout/service.go:304-311`; `payout/solana_transfer.go:116-120` | If `Transfer` submits the tx but the RPC response is lost (timeout) and the tx actually lands, the code `Release`s coins back to the agent **and** sets `failed`; USDC left the hot wallet and the agent kept the coins. Chain sends aren't key-idempotent. Overlaps W5. | ⚠ Reported |
| **G1** | 🟠 HIGH | `SettleHeld` mis-pays a **held multi-winner Mafia table** as winner-take-all | `wallet/money.go:66-73` + `mafia/service.go:263-291` | Admin-clearing a held paid Mafia table routes through the generic 2-player `SettleHeld` (`pool→set.Winner`), but Mafia is a team split via `ComputeRewards`. One seat is over-paid, the rest under-paid, or the wrong seat entirely. | ✅ Verified |

### Custody / auth assurance
| ID | Sev | Title | Location | Impact | Status |
|----|-----|-------|----------|--------|--------|
| **W3** | 🟠 HIGH | Hot-wallet signing key is a **plaintext env var** (no KMS/`secretbox`), no float cap or cold sweep | `config.go:217` → `main.go:398` → `payout/solana_transfer.go:35-39` | Any env/log/core-dump exposure drains the entire hot wallet. `secretbox` (AES-GCM) exists but is used only for dev bearer tokens. Key is correctly never logged. | ⚠ Reported |
| **W2** | 🟠 HIGH | Withdrawal destination is the **unverified Privy wallet hint** (no ownership proof) | `store/payout_repo.go:61-69` ← `payout/service.go:153-161` | No signed-nonce proof that the payout address belongs to the user before the first withdrawal. Chained with W1, real USDC goes to an attacker-controlled wallet. | ⚠ Reported |
| **P1** | 🟠 HIGH | The "certified" **ranked-entry gate never exercises the agent's `/turn`** | `manifest/verify.go:78-149`; `manifest/service.go:83-92`; `devplatform/certify.go` | `Verify` proves `/health` + a handshake + echoed game names — never a driven turn. A stub that returns `{healthy}`+`{accepted,supportedGames}` passes cert, enters funded ranked play, then fallback-loses every turn. The badge overstates assurance and lets broke/dead agents into staked matches. | ⚠ Reported |
| **P2** | 🟠 HIGH | A **single env boolean disables the entire SSRF defense** | `config.go:227`; `main.go:210,224`; `agentclient.go:124` | `AGENT_VERIFY_ALLOW_PRIVATE` bypasses the whole private-IP block **and** allows `http://` for every manifest endpoint. One flag copied from a dev `.env` into staging turns the platform into an SSRF proxy against internal services + cloud metadata. | ⚠ Reported |

### Process & real-time robustness (for scale)
| ID | Sev | Title | Location | Impact | Status |
|----|-----|-------|----------|--------|--------|
| **R1** | 🟠 HIGH | ~15 background workers have **no panic recovery** — one panic kills the whole process | `events/events.go:93-108`, `webhook/webhook.go:168-176`, `match/sweeper.go:27-44`, +12 loops in `main.go` | A single nil-deref/bad-assert in any tick (event dispatcher, sweepers, matcher, reconcilers, season roller…) aborts the process, dropping all in-flight matches + SSE. Only 4 loops (payout/walletrecon/deposit/agentgw) already `recover()`. **Found independently by 2 auditors.** | ⚠ Reported |
| **R2** | 🟠 HIGH | Mafia long-poll **lost its safety-tick backstop + version compare** | `mafia/service.go:358-369` vs `match/service.go:655-678` & `monopoly/service.go:315-337` | A single `select{wake / After(timeout)}` with no 2s backstop and no `stateVersion` compare. A missed/pre-subscribe wake or a Redis blip blocks the caller the full 15s even though state changed 100ms in. Goofspiel & Monopoly do this correctly; Mafia regressed. | ⚠ Reported |
| **R3** | 🟠 HIGH | **DB pool of 20** shared by ~18 background loops + all request/stream traffic | `config.go:192`; `store/postgres.go:16` | Webhook dispatcher (8) + health Monitor (8) alone can hold 16/20, starving player turns and spectator resume during a delivery burst. | ⚠ Reported |
| **R4** | 🟠 HIGH | **No global SSE connection cap** per instance (only 1000/match) | `spectator/hub.go:44-59` | 10k spectators = 10k goroutines/conns/buffers unbounded on one instance; compounds C1's reconnect storm. | ⚠ Reported |
| **G2** | 🟠 HIGH | Goofspiel & Mafia **timeout path still hard-requires the Redis lock** (no lockless fallback) | `match/service.go:578-582`; `mafia/service.go:293-298` | `Act` was made lockless-with-OCC and Monopoly's `HandleTimeout` got the matching fallback — but Goofspiel/Mafia `HandleTimeout` still `return err` on lock failure. During a Redis outage a match whose players go idle **never times out, never finalizes, escrow stays held** — the exact money-stuck case the guarantee was meant to cover. | ⚠ Reported |

---

## 3. Medium & notable findings (post-launch fast-follow)

| ID | Sev | Title | Location | Note |
|----|-----|-------|----------|------|
| **SEC-M3** | 🟡 MED | `PLATFORM_ENGINE_PRIVATE_KEY` not required in prod → **unsigned** domain events to the Super Admin stream | `config.go:334`, `main.go:143-145` (warn only) | Admin can't distinguish genuine from forged events. Require it in `validate()` under `IsProd()`. |
| **M4** | 🟡 MED | Admin balance-adjust uses a **fresh random idempotency key every call** | `walletadmin/service.go:108` → `wallet/money.go:184` | Double-click / client retry double-applies the credit/debit. Require a client-supplied/derived key. |
| **M5** | 🟡 MED | Coin peg derived by **integer division** `100 / CoinCents` | `main.go:431-432` | `COIN_CENTS=3`→33 coins/USDC (asymmetric peg); `>100`→0→silent fallback to 100. Validate `100 % CoinCents == 0` at boot; single source of truth for both ramps. |
| **M6** | 🟡 MED | `Settle` (ledger) and `Finish` (status) are **not atomic** | `match/service.go:502,531` | Crash between them leaves an `active` match whose escrow already paid — no money lost (idempotent re-finalize) but coins stuck until swept. Fold into the outbox or add a re-finalize sweeper. |
| **P3** | 🟡 MED | `movesig` × push-play are **mutually exclusive and wedge** | `sandbox/pushplay.go:210`; `match/service.go:369-383` | Push-play submits with empty signature; a signing-key agent's every move → `ErrSignatureRequired` → driver stops → match sits active until abandoned. Exempt the platform-driven seat or reject up front. |
| **P4** | 🟡 MED | Orphaned `claimed` queue rows → path to a **second escrow** | `matchmaking/matcher.go:104-110`; `matchmaking.go:133-157` | `MarkMatched` failure after `CreatePaired` strands rows; `Enqueue` resets to waiting with no active-match check → re-pairs + double-stakes. Add a reconciler + reject enqueue when already in a live match. |
| **P5** | 🟡 MED | Broke/ineligible agent **thrashes the matcher** and delays honest pairs | `matcher.go:86-103` | Failed `CheckJoin` doesn't mark the agent used for the tick → retries every candidate every tick, flapping legit opponents. Skip failed agents per pass; back off after N. |
| **G3** | 🟡 MED | Mafia has **no OCC** — concurrency rests entirely on the Redis lock | `mafia/service.go:199-241`; `mafia_repo.go:213-270` | Lease expiry mid-persist → duplicate-seq → raw 500 (no corruption). Map unique-violation → `ErrConcurrentUpdate` + retry; also unblocks G2. |
| **RT-M1** | 🟡 MED | Outbox→Redis mirror **poison**: a ~10s Redis outage permanently drops domain events | `store/platform_events.go:47-62`; `events/events.go:81,111-128` | Single shared `published_at` across all handlers + 10-attempt cap → event excluded from `Unpublished` forever after Redis blip. Per-handler cursor, or make the mirror non-failing, or backfill. |
| **RT-M2** | 🟡 MED | Webhook `/event` delivery **unordered per agent+match**; head-of-line stall on slow first-contact | `webhook/webhook.go:155-176` | `tick` blocks on `wg.Wait()`; a burst of black-hole endpoints ties up all 8 workers for 5s each. Decouple claim cadence from batch completion; agents must order by `Seq`. |
| **RT-M3** | 🟡 MED | Webhook enqueue **not transactional** with the state change; push-play drives in-memory, not resumed on restart | `agentwire/agentwire.go:89-107` | Crash between state commit and enqueue loses the `/event`/`/game-end`; a restart abandons active sandbox drives. Enqueue in the producer tx for real-stakes; add a resume sweeper. |
| **SEC-M1 / W4** | 🟡 MED | Sensitive money/outbound routes have **no rate limit** | `payout/handler.go:39`, `solanadeposit/handler.go:27`, `manifest/handler.go:41`, `identity/handler.go:69` | Only register/login are limited. `manifest/verify` drives an outbound probe (amplification); `deposits` mints an unbounded reference/row per call. Add per-user `RateLimit`. |
| **SEC-M2** | 🟡 MED | Rate limiting **fails open** on Redis error | `middleware/ratelimit.go:38-40` | A Redis blip bypasses login/register limits → brute-force window. Fail closed (or local counter) for auth buckets specifically. |
| **W5** | 🟡 MED | Broadcast→status-write crash strands funds in `processing` (recovered only after ≥1h recon) | `payout/service.go:304-319` | No double-spend (atomic claim guards) but not exactly-once. Reconcile `processing` vs chain on startup + short ticker. Related to M3. |
| **S1** | 🟡 MED | SDK signing golden vector is **un-guarded on the Go side** | `agentclient/sign_test.go:24-57` (self-recompute); golden pinned only in JS/Python | A Go canonicalization change passes Go's tautological test + the SDKs' unchanged hardcoded golden → ships → every hosted-HTTP match fails `bad_signature`. Assert the same golden fixture in Go. |

**Notable INFO / resolved (verified):**
- **G5 — the "goofspiel: card is not in the seat's hand" sweeper error is resolved / cannot fire** in current code (`ForceTimeout` only errors on empty hand, which can't occur before the match leaves the active sweep set). It never corrupted real matches; a hostile out-of-hand card is correctly rejected by `Seal`. ✅
- **`/v1/matches/live` Monopoly-pollution is already fixed** via the `m.game='goofspiel'` filter (`store/spectator_repo.go:27`). ✅
- **M2 (dev-payments-fail-open-in-prod) is a FALSE POSITIVE** — `main.go:512-538` refuses to boot in prod without real Stripe config. ❌ Corrected. (Still worth gating the `DevMode` flag on `!IsProd()` for defense-in-depth.)
- **Beta WebSocket transport does no per-message HMAC/replay** (auth once at register) — the extensive signing machinery protects the *legacy* HTTP path, not the primary Beta socket path. Architectural note, not a bug: WS is authenticated + TLS + engine re-validates every move.
- Low-severity, tracked but non-blocking: W6/W7/W8/W9 (deposit expiry drift, inert admin `confirmations` knob, `exp`-not-required, 10s settings-cache kill-switch lag), M7/M8/M9 (divergent `Withdrawable` display, single-signature-per-session deposit, stale "Onavion" brand string), G4 (Monopoly cosmetic `entryFee`), P6/P7/P8 (register-metadata XSS, uncapped mafia chat, no verify TTL), L1-L5 & SEC L1-L4 (metrics auth, JWT revocation, anti-fraud swallowed errors, platform-token `iat` bypass, SSE full-history replay).

---

## 4. Remediation plan (phased, each independently shippable + verifiable)

### P0 — Launch blockers (do first; nothing funded ships without these)
1. **W1 Privy takeover.** Stop linking on a client-supplied email. Either key strictly on `privy_user_id`
   (create-new otherwise), **or** fetch the *verified* linked email from Privy's server API (app secret)
   before linking, **or** require an authenticated in-app link flow. Add a regression test: attacker DID +
   victim email must NOT return the victim's session.
2. **C1 write-deadline.** Set `WriteTimeout: 0` on the server (or serve streaming routes from a second mux
   with `WriteTimeout=0`); in every SSE/long-poll handler clear the deadline after headers
   (`rc.SetWriteDeadline(time.Time{})`) and re-arm a per-write stall guard (`now+30s`) before each frame.
   Verify: a `/watch` stream stays open >60s and receives the 25s keepalive.

### P1 — Money-edge correctness (close the loss/double-pay paths)
3. **M1 refund clawback.** Key reversals on cumulative-refunded delta (reverse `wantCoins − alreadyReversed`),
   not a flat PI key. Test multi-partial-refund and dispute-then-refund.
4. **M3 / W5 Solana broadcast ambiguity.** On a `Transfer` error do **not** release; leave `processing` and
   let a chain-reconciler decide burn-vs-release by the derived signature. Reconcile `processing` on startup +
   a short ticker (not the 1h window).
5. **G1 held multi-winner settlement.** Give Mafia (and future Monopoly) a game-aware held-release that
   re-derives payouts via `ComputeRewards`; never route a multi-winner game through 2-player `SettleHeld`.
6. **M4 admin-adjust idempotency** + **M5 peg validation** (boot-time `100 % CoinCents == 0`) + **M6**
   re-finalize sweeper (or fold settle into the outbox).

### P2 — Custody & assurance
7. **W3 hot wallet** → load key from KMS or `secretbox`-at-rest; add a float cap + scheduled cold-sweep +
   low/high-balance alerts.
8. **W2 withdrawal ownership** → require a signed-nonce challenge proving the destination wallet before the
   first payout; re-verify on address change.
9. **P1 certification** → add at least one real driven `/turn` probe (a short match through `remoteplay`/the
   socket) to the verify flow before `active`; make `RequireCertified` depend on it.
10. **P2 SSRF flag** → split `AllowPrivate` from `AllowInsecureEndpoint`; refuse to start if `AllowPrivate`
    is set while `ENV=production`; log loudly at boot when either is on.
11. **SEC-M3** require `PLATFORM_ENGINE_PRIVATE_KEY` in prod; **SEC-M1/W4** add per-user rate limits on
    deposit/withdraw/verify/key-rotation; **SEC-M2** fail-closed auth rate limits.

### P3 — Process & availability robustness
12. **R1** shared `platform.SafeGo`/`safeTick` with `recover()` on every background loop; a `WaitGroup` join
    on the money workers at shutdown.
13. **R2** port the Goofspiel long-poll loop (subscribe → version → `select{ctx/wake/After(min(rem,2s))}`) to
    Mafia; **G2/G3** port Monopoly's lockless-fallback timeout to Goofspiel/Mafia + add Mafia OCC.
14. **R3** raise DB pool and split background-worker pool from request pool; **R4** add a hub-wide SSE cap
    (`ErrTooManyWatchers` + `Retry-After`) and expose the existing `sse_subscribers` gauge.

### P4 — Eventing durability & SDK guard
15. **RT-M1** per-handler delivery cursor (or non-failing Redis mirror + backfill); **RT-M2** decouple
    dispatcher claim from batch completion; **RT-M3** transactional enqueue for real-stakes + push-play resume
    sweeper. **S1** shared Go/JS/Python signing fixture + a live "Go signs / SDK verifies" e2e test.

### P5 — Low / info hardening
16. The tracked low-severity list above (metrics auth, JWT revocation, anti-fraud error logging,
    platform-token `iat`, SSE incremental replay, register-metadata validation, mafia chat cap, deposit
    label/brand, `confirmations` knob).

---

## 5. Low-latency / real-time architecture plan (industry-grade)

The design instincts here are already strong — SSE (not WS) for spectators, drop-slow non-blocking fan-out,
`X-Accel-Buffering: no`, no compression on streams, Redis pub/sub long-poll with a safety-tick backstop,
async webhooks off a durable outbox, chain confirmation off the hot path. The plan below turns "correct
design" into "smooth at scale."

**Current latency budget (per path):**
- **Spectator SSE:** steady-state ≈ flush + network RTT (single-digit ms in-process) — *excellent*, but
  **destroyed by C1** (15s teardown + full-history replay per reconnect, L1).
- **Pull-agent wake (`state?wait=true`):** Goofspiel/Monopoly <5ms via pub/sub, 2s backstop — *good*;
  **Mafia is the outlier** (R2, up to 15s stall).
- **Push-play turn:** agent decision time + one HTTP RTT (socket transport amortizes TLS); `/event` +
  `/game-end` fully async — *correct separation*.

**Highest-ROI moves, in order:**
1. **Fix the write-deadline model (C1).** Single biggest real-time win. `WriteTimeout=0` +
   per-frame `SetWriteDeadline` to reap only black-hole clients. This is the standard Go SSE pattern.
2. **Serve `Last-Event-ID` resume from memory.** Add a per-match ring buffer of the last N frames in the
   hub; push the `seq > lastSeq` filter into SQL for the cold path (L1). Eliminates the reconnect DB
   amplifier and makes resume O(1).
3. **Split DB pools + size workers (R3).** A request pool (e.g. 30) and a background-worker pool (e.g. 20)
   so a webhook/monitor burst can never starve player turns; cap `webhook.Workers × instances` against the
   worker pool.
4. **Uniform low-latency long-poll (R2/G2/G3)** across all three games: pub/sub wake + 2s backstop +
   version compare + lockless-with-OCC — so a Redis blip degrades to 2s, never 15s, and never wedges money.
5. **Process-local notifier fallback (L2).** So single-instance / Redis-down deployments keep sub-second
   wake-ups. Consider Postgres `LISTEN/NOTIFY` as a wake path that shares the DB you already trust for
   correctness — removes Redis from the turn-latency critical path entirely.
6. **Backpressure & shedding (R4).** Hub-wide connection ceiling + `Retry-After`; drive autoscaling off the
   existing `sse_subscribers` / `sse_dropped_slow_total` gauges.
7. **Batch / coalesce chatty events.** For Monopoly/Mafia, a batch-notification frame (array of events with
   `Seq`) cuts per-item HTTP/webhook overhead; safe because every `/turn` view is self-contained (events
   are an optimization, not correctness). Decouple the webhook dispatcher claim cadence from batch
   completion to kill head-of-line stalls (RT-M2).
8. **Robustness as a latency property (R1, M4/M6).** Panic-recovered workers + a re-finalize sweeper keep
   the money engine and event fan-out alive so tail latency doesn't spike to "process restart."

**Keep as-is (verified good, do not churn):** SSE over WebSocket for spectators; drop-slow fan-out under
`RLock`; no stream compression; the circuit-breaker + health-monitor webhook dispatcher; chain confirmation
as pollers with panic recovery off the request path; the CDN-cacheable `/live` reads.

---

## 6. What was proven sound (so effort isn't wasted re-checking)

- **Double-entry ledger:** balanced `Σ==0` enforced pre-write; all postings in one `FOR UPDATE`-locked,
  `UNIQUE(idempotency_key)`-guarded tx; DB `CHECK` against negative protected wallets; reconciler pages on
  drift. No way found to unbalance the books or double-credit on a normal replay.
- **Escrow exactly-once** (stake/settle/refund/hold/payout/release each idempotency-keyed); **net-winnings-only
  withdrawal** correctly closes buy→withdraw laundering; **Stripe webhook** real HMAC + persist-first replay.
- **Deposit detection:** credits only on `finalized` + `mint==USDC` + lands in platform ATA; 32-byte CSPRNG
  reference (unforgeable); idempotent on tx signature twice over; credit-then-record is crash-safe.
- **Withdrawal double-broadcast prevented** by an atomic `requested→processing` claim.
- **Game engines** are pure & server-authoritative: seeded HMAC RNG (no wall-clock/global RNG), sorted
  map-iteration, thorough illegal-move rejection, guaranteed termination + settle-exactly-once (OCC).
- **SSRF hardening is real** (resolved-IP check → DNS-rebinding-safe, no redirects, bounded body); **WS caps**
  enforced pre-upgrade; **agent trust boundary held** (every move re-validated by the engine).
- **SDK HMAC signing byte-identical Go/JS/Python** (proven by execution); constant-time compare in both langs;
  no secret logged.
- **Cross-cutting:** no IDOR/SQLi found; token-derived identity everywhere; alg-confusion-safe JWT; fail-closed
  prod guards; no schema drift; complete down-migrations; hot-path indexes present.

---

_Auditors: 7 parallel Opus domain agents + hand-verification of all CRITICALs and top money/engine HIGHs.
Baseline build/vet/unit-tests green at time of audit._
