# Wallet / Money-System Security Audit & Remediation Plan

**Scope:** end-to-end money path — **buy coins** (Solana USDC deposit) → **win/lose coins** (stake ledger, escrow, rake) → **cash out** (USDC payout from hot wallet). Backend: `Agentic_World/backend` (Go).
**Method:** 5 parallel deep audits (deposit, ledger+game, withdrawal+wallet-proof, secrets/auth) + web research of current (2024–2026) industry standards; top findings hand-verified against source.
**Date:** 2026-07-14.

---

## TL;DR

- **No remotely-exploitable CRITICAL theft path was found.** The scariest attack classes — withdraw-to-a-victim's-wallet, double-broadcast/double-pay, admin-auth bypass, IDOR/wallet leakage, Privy account-takeover — are **all correctly closed**.
- **3 HIGH issues** need fixing before real money at scale: an **admin-triggered escrow double-spend** (dispute→refund on an already-settled match), a **prod config that allows the hot-wallet key in plaintext**, and a **deposit path that trusts a single RPC** with no reference re-verification.
- **~11 MEDIUM** correctness/fraud/AML/ops issues, **~11 LOW** hardening items.
- Everything below is mapped to the industry control it satisfies (see `## Industry controls` at the end).

Severity = money-at-risk × likelihood. "Verified" = I re-read the code and confirmed the finding.

---

## 🔴 HIGH

### H1 — Prod allows the hot-wallet PRIVATE KEY as a plaintext env var (encryption-at-rest not enforced) — *verified*
`internal/config/config.go` `validate()` + `cmd/server/main.go:461-463`
`WithdrawalsSolana()` accepts **either** `SOLANA_HOT_WALLET_SECRET` (plaintext base58 key) **or** the secretbox-encrypted form. `validate()` enforces JWT/CORS/platform-key rules but has **no** rule requiring the encrypted form in prod — it only `log.Warn`s and boots. The full payout signing key can therefore live in the process environment, orchestrator manifests, and crash dumps.
**Impact:** anyone with env/manifest/process read access drains the hot wallet. Private-key exposure was **43.8% of all stolen crypto value in 2024** (Chainalysis) — the #1 real-world loss vector.
**Fix:** in `validate()`, when `IsProd() && WithdrawalsSolana()`, **require** `SolanaHotWalletSecretEnc != ""` and **reject** a non-empty plaintext `SolanaHotWalletSecret`. Fail closed. Keep the master key (`SOLANA_HOT_WALLET_ENC_KEY`) in a *different* secret store than the ciphertext (KMS/secret-manager), not next to it in the same env file.
**Industry:** §1 key management.

### H2 — Settle and Refund are not mutually exclusive → dispute-refund double-spends the shared escrow — *verified*
`internal/wallet/money.go:87,110` (`settle`, key `settle:{match}`) vs `:130,145` (`Refund`, key `refund:{match}`); `internal/store/antifraud_repo.go:88` (`OpenDispute` — **no match-status guard**); `internal/antifraud/service.go:135-147` (`ResolveDispute` refund/release).
Escrow is a **single shared wallet** holding every live match's stakes; the DB `CHECK` only stops the *aggregate* balance going negative. Settle and refund use **different idempotency keys**, so the ledger treats them as unrelated.
**Exploit A:** a match settles cleanly (winner paid, escrow −pool). Any authenticated user files a dispute on that finished match (`POST /v1/disputes` has no status check). An admin resolves `refund` → `Refund` posts `refund:{match}`, debiting escrow **another** pool and returning both stakes → winner keeps payout **and** players refunded; the second pool is silently drained from *other* live matches' escrow.
**Exploit B:** two disputes on a held match, admin resolves one `release` + one `refund` → double payout.
**Impact:** money-integrity / fund loss to platform + other players. Requires an admin action, but there is **no state guard** preventing it (a tricked/careless/compromised admin, or a bogus dispute, is enough).
**Fix:** unify settle+refund under one per-match payout state (single `payout:{match}` key, or a `matches.payout_state` column checked-and-set atomically before either posts); make `OpenDispute`/`ResolveDispute` refuse a match whose payout is already terminal.
**Industry:** §4 atomic/exactly-once settlement.

### H3 — Deposit crediting trusts a single RPC with no reference re-verification / second source
`internal/solanadeposit/service.go:184-216,254-266`; `internal/blockchain/client.go:112-214`
`processSession` trusts `getSignaturesForAddress(reference)` to bind a signature to a session and `getTransaction` for the amount, but **never re-verifies that `sess.Reference` actually appears in the transaction's `accountKeys`**, uses no second confirmation source, and does not assert `confirmationStatus=="finalized"` on the signature entry itself.
**Impact:** a compromised/MITM'd/malicious `SOLANA_RPC_URL` can return, for the attacker's reference query, the signature of *any* real deposit into the platform ATA + a forged `getTransaction`, minting coins backed by no real deposit → withdrawable USDC. **No exploit under an honest RPC** (references are 32-byte CSPRNG secrets), so this is a trust-boundary hardening issue, not a remote user exploit.
**Fix:** in `GetTransaction`, parse `message.accountKeys` and assert `sess.Reference ∈ accountKeys` before crediting; require finalized on the signature entry; consider a second independent RPC for large credits and an absolute per-tx amount sanity cap. Pin to a trusted/self-hosted RPC.
**Industry:** §2 deposit safety (reference is *not* proof of validity).

---

## 🟠 MEDIUM

- **M1 — Stripe flat fee (25¢ default) charged on the Solana rail — *verified, on by default*.** `internal/payout/service.go:82-88` (`quote` always adds `StripeFeeFlatCents + StripeFeePct%` regardless of chain; `solana()` helper exists one line above, unused). User receives 25¢ less USDC than the coins burned represent, and `stripe_clearing` over-credits 25¢ per payout → ledger↔on-chain-treasury peg drifts every withdrawal. **Fix:** zero Stripe fees when `Chain==ChainSolana` (or a rail-specific network-fee field). *Industry §3/§4.*
- **M2 — No separation of duties: an admin can approve their own withdrawal.** `internal/payout/handler.go:139-149` + `service.go:229-261` never compares approver to `w.Owner`. **Fix:** reject `Approve` when `w.Owner == adminUserID`; require a Platform token for admin-owned withdrawals (maker-checker). *Industry §6.*
- **M3 — Two disjoint withdrawal minimums silently disagree — *verified in testing*.** Env `WITHDRAW_MIN_COINS` (`payout/service.go:191`) **and** `wallet_settings.min_withdraw_coins` (`walletadmin/service.go:168`, DB default 500). The DB gate silently overrode the env during testing. **Fix:** unify to one source of truth (or document precedence + surface both). *Industry §6.*
- **M4 — Distinct-owner sybil chip-dumping launders deposits into withdrawable "winnings".** `store/wallet_repo.go:248-265` (withdrawable = net winnings, good) but the payout gate only holds *same-owner* matches (`antifraud/service.go:70`); two different-owner accounts (same human) can funnel deposits into winnings for the cost of rake. `IsColluding` needs ≥5 games & ≥85% win-rate, only *flags future* payouts, never claws back. **Fix:** low-history velocity/graph limits before first payout; hold/clawback path when a ring flag lands post-payout; device/IP/identity fingerprinting. *Industry §5/§7.*
- **M5 — Mafia same-owner check only compares the creator (`Players[0]`).** `internal/mafia/service.go:138`; `RosterSize=12`. If a *different* owner created the table, one owner can seat the other 11 chairs with distinct agents → coordinated majority. Money is saved only by the review-only gate hold (which a single erroneous `release` pays out). **Fix:** reject a join if the owner matches *any* seated player, not just `Players[0]`. *Industry §5.*
- **M6 — `CheckJoin` limits are TOCTOU.** `internal/wallet/limits.go:16-95` runs read-only, separate from the later stake txn; concurrent joins to *different* matches race the check → `max_concurrent_matches`, daily/session loss, and min-balance reserve bypassable. Hard ledger non-negativity still holds (policy bypass, not theft). **Fix:** enforce concurrency/reserve inside the staking transaction, or serialize per-agent admission. *Industry §4.*
- **M7 — Deposit amount parse error swallowed + int64 overflow in coin conversion.** `internal/blockchain/client.go:151-154` (`ParseInt` error discarded → `MaxInt64`); `solanadeposit/service.go:60-62` (`base*CoinsPerUSDC` unguarded). **Fix:** propagate the parse error (skip credit); use `math/bits.Mul64`/`big.Int` + a sanity cap. *Industry §2/§4.*
- **M8 — Deposit owner-match accepts arbitrary attacker-created token accounts.** `solanadeposit/service.go:261` counts USDC in *any* token account whose `owner==PlatformOwner`, not just the canonical ATA (funds still platform-controlled → not theft, but breaks reconciliation/custody tracking). **Fix:** match the exact `PlatformATA` only (or the derived canonical ATA). *Industry §2.*
- **M9 — In-flight deposit lost if it finalizes at/after session expiry; no reclaim path.** `solanadeposit/service.go:168-173` expires a session before processing; a payment that finalizes just after the 30-min window is never credited. User fund-loss, no admin re-credit surface. **Fix:** don't expire a `detected` session; add a grace window + admin re-credit keyed on tx signature. *Industry §2.*
- **M10 — Withdrawals can strand in `processing`/`broadcasted` with escrow frozen; no auto-recovery.** `payout/service.go:307-347` + watcher scans only `broadcasted` (`:361`). A crash between claim and status-write freezes the user's coins with no self-healing. Not a double-pay (the no-op is intentional). **Fix:** persist the deterministic signature at claim time; add an age-based reconciliation sweep for stale `processing` that resolves on-chain then releases/advances. *Industry §3.*
- **M11 — Email/password users cannot withdraw — *verified in testing*.** `wallet_address` (the payout hint) is only set by Privy login; there's no link endpoint. `verify` sets `verified_wallet_address` but `Request` needs the login `wallet_address` present + equal (`payout/service.go:150-171`). Non-Privy users are locked out. **Fix:** add an authenticated "link withdrawal wallet" step (which the W2 challenge/verify already effectively is) that sets the hint, or make `verified_wallet_address` sufficient. *Industry §6.*

---

## 🟡 LOW / hardening

- **L1 — secretbox master key: bare SHA-256, no entropy floor/validation.** `internal/secretbox/secretbox.go:37`; no prod length check on `SOLANA_HOT_WALLET_ENC_KEY`. A weak passphrase is brute-forceable against the ciphertext. **Fix:** enforce ≥32-byte/placeholder check in prod (mirror JWT rule); keep ciphertext and master key in separate trust stores.
- **L2 — Stale Privy docstrings describe the removed email-linking (takeover) behavior.** `internal/identity/privy.go:39-42`, `internal/auth/privy.go:17`. Code is safe (keys on `privy_user_id`), but the doc is a landmine for reintroduction. **Fix:** correct the docstrings.
- **L3 — `SOLANA_COMMITMENT=confirmed` knob allows reorg rollback of irreversible credits.** `config.go:233` (default `finalized`, safe). **Fix:** force finalized for crediting, or gate `confirmed` behind non-prod.
- **L4 — Mint has no amount ceiling.** `internal/wallet/handler.go:120-143` (safe by `ALLOW_MINT` off-prod + ownership). **Fix:** cap amount + assert non-prod at runtime (defense-in-depth).
- **L5 — Chargeback clawback reads balance outside its txn.** `wallet/money.go:265-292` (fails safe; can wrongly block a legit chargeback). **Fix:** compute recovered inside the locked txn.
- **L6 — Payout-gate owner resolution uses INNER JOIN, dropping unresolved-owner seats.** `store/antifraud_repo.go:22-30` — defeats the intended fail-closed `owner_unresolved` hold. **Fix:** LEFT JOIN so a missing owner surfaces as `""` and holds.
- **L7 — Wallet-verify signed message lacks user/expiry binding.** `walletverify.go:24,105` (safe today via per-user single-use nonce; weakens phishing resistance). **Fix:** embed user id + expiry in the signed message and verify server-side.
- **L8 — `ClearChallenge` error swallowed after verify.** `walletverify.go:111` (no replay risk; observability gap). **Fix:** log it.
- **L9 — `platformsign` verifier fails OPEN when key unset.** `platformsign/sign.go:83-85` (safe only because prod `validate()` requires the keys). **Fix:** fail closed by default.
- **L10 — Deposit: no leader election / row lock; redundant multi-instance polling + unbounded per-tick RPC work.** `store/solanadeposit_repo.go:76-85`. **Fix:** `SELECT … FOR UPDATE SKIP LOCKED` or single-worker leader election.
- **L11 — `requested_at` serialized as zero-value in the `POST /v1/withdrawals` response** (cosmetic; DB + admin list are correct).

---

## ✅ Verified SAFE (do not regress these)

**Withdrawal / payout:** withdraw-to-victim-wallet **closed** (W2: CSPRNG nonce, per-user, single-use, 10-min TTL, ed25519 over exact challenge; funds only reach a wallet the caller proved) · double-broadcast/double-pay **closed** (atomic OCC `requested→processing` claim before transfer; Stripe idempotency key) · escrow terminal states mutually exclusive & idempotent · broadcast-ambiguous holds (never releases) · admin authz defense-in-depth + Platform-token Ed25519 forgery-resistant + IDOR closed on `GET /v1/withdrawals/{id}`.
**Deposit:** double-credit/replay **closed** (idempotent on tx signature at ledger + `solana_deposits` PK) · credits the **actual on-chain amount**, not the trusted session value (underpayment can't over-credit) · mint + destination-ATA verified · finalized default · reference is 32-byte CSPRNG, owner-scoped.
**Ledger:** balanced Σ=0 enforced before write · `FOR UPDATE` ascending-id locking (deadlock-free) · idempotency via UNIQUE key · non-negative CHECK on agent+escrow · atomic all-or-nothing stake/settle · withdrawable double-capped (net winnings ∧ balance); ties net-zero.
**Auth/secrets:** no secret/key/token/wallet ever logged or returned · Privy account-takeover **closed** (keys on `privy_user_id`; email non-authoritative) · Privy token validation correct (ES256, iss/aud/exp) · JWT hygiene + strict scope firewall (agent↛owner, user↛admin) · API keys bcrypt+pepper, shown once, prefix-only reads, revocable · CORS strict, dev footguns fail closed in prod.

---

## Remediation plan (phased) — *do before scaling real money*

### Phase 0 — Launch-blockers (fail-closed config + no-compromise-needed integrity)
1. **H1** — enforce encrypted hot-wallet key in prod (`validate()` fail closed; reject plaintext); document master-key in a separate store. *(~small, config)*
2. **H2** — unify settle/refund under one per-match payout state; guard dispute open/resolve against already-terminal payouts. *(~medium, ledger + antifraud)*
3. **M1** — rail-aware fees (no Stripe fee on Solana). *(~small)*
4. **M3** — unify the two withdrawal minimums. *(~small)*

### Phase 1 — Deposit & payout hardening
5. **H3** — reference-in-accountKeys re-verification + explicit finalized-on-signature + amount sanity cap; pin trusted RPC (+ optional 2nd source for large credits).
6. **M7** — parse-error propagation + overflow-safe coin conversion.
7. **M8** — accept only the canonical platform ATA.
8. **M9** — don't expire detected sessions; grace window + admin re-credit path.
9. **M10** — persist signature at claim; stale-`processing` reconciliation sweep.
10. **M2** — separation of duties on withdrawal approval (requester ≠ approver).

### Phase 2 — Fraud / AML / defense-in-depth
11. **M5** — Mafia: reject join if owner matches any seated player.
12. **M6** — enforce join limits inside the staking transaction (kill TOCTOU).
13. **M4** — sybil/velocity limits + device/IP/identity fingerprinting; clawback/hold-on-flag; keep net-winnings cash-out (add play-through/turnover if buy→withdraw must be allowed).
14. Withdrawal **velocity limits + step-up 2FA + new-address cooldown** (CEX-standard).
15. **L1, L3, L4, L6, L7, L9** — entropy floor, confirmed-knob lockout, mint ceiling, LEFT JOIN, message binding, fail-closed verifier.
16. **Reconciliation job:** on-chain hot-wallet USDC ≥ outstanding withdrawable liability; coin-conservation check; alert on drift (also catches M1-class bugs).

### Phase 3 — Custody & compliance (organizational)
17. Cold/warm/hot split with a documented sweep threshold; move signing to **KMS/HSM or MPC** (key never in app memory).
18. KYC before cash-out; transaction monitoring / SAR-style flagging; Travel-Rule readiness for the binding jurisdiction(s).
19. Fix docs (**L2**), add error logging (**L8**), response field (**L11**).

---

## Industry controls (mapping key)

- **§1 Key management** — hot/cold split, HSM/MPC/KMS, AES-GCM/secretbox at rest with master key in a separate store, never-log-secret, rotation. *(Chainalysis 2024: key compromise = 43.8% of stolen value; WazirX/Bybit = signer deception.)*
- **§2 Solana deposit** — credit only at `finalized`; validate mint + ATA + decimals; reference proves *which* session, **not** validity (re-verify independently); idempotent on tx signature; beware Token-2022 fee/hook if widening mints.
- **§3 Solana payout** — never retry before blockhash expiry (double-spend); idempotent state machine; ambiguous-send never auto-refunds; destination validation; **allowlist + step-up 2FA + cooldown**; debit/escrow at request time.
- **§4 Ledger** — double-entry append-only; atomic conditional decrement (no TOCTOU); idempotency keys; atomic settlement; negative/overflow guards; reconciliation vs on-chain.
- **§5 Game economy** — block self-dealing/chip-dumping (device+IP+identity); collusion/one-sided-loss detection; **cash-out gated to net winnings / play-through**; Sybil & bonus-abuse controls.
- **§6 API security (OWASP 2023)** — BOLA/IDOR + BFLA closed; identity from token only; mass-assignment whitelist; rate/velocity limits; step-up auth for money movement; maker-checker on approvals.
- **§7 Compliance** — KYC before cash-out; net-winnings/play-through as AML control; transaction monitoring/SAR; FATF Travel Rule (EU TFR is zero-threshold since Dec 2024).

*Primary sources:* Solana exchange integration guide & confirmation guide; OWASP API Top 10 2023; FATF 2024 VA/VASP update; Cantina "signature verification risks in Solana"; Chainalysis/The Block 2024 incident data. (Full URLs in the research transcript.)
