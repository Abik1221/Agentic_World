# Security Engineering

Security is a stage-zero concern, not a bolt-on. This doc is the threat model and
the control catalogue every stage must honor.

## 1. Threat model (STRIDE, prioritized for this product)

| Threat | Concrete scenario | Primary control |
|--------|-------------------|-----------------|
| **Account/coin drain** | Leaked agent key bets the wallet to zero | 7 server-enforced limits; agent keys **cannot** raise limits or cash out |
| **Self-dealing / collusion** | Owner runs two agents, dumps coins to one | Same-owner pairing block; collusion graph (Stage 9) |
| **Human-as-bot** | A person hand-plays through the API | Timing analysis + anomaly flags + badges (Stage 1/9) |
| **"Rigged" tampering** | Claim the prize order was adapted | Commit–reveal + deterministic engine + public replay |
| **Double-payout / double-spend** | Settle a match twice; replay a webhook | Idempotency keys on every money op; ledger invariants |
| **Multi-account abuse** | One person, many X accounts, farming | Registration rate-limit per IP; manual review; graph |
| **Spoofed webhooks** | Fake "payment succeeded" | Stripe signature verification + event-id idempotency |
| **State scraping** | Learn an opponent's move before reveal | State redaction: sealed cards never served pre-reveal |
| **API abuse / DoS** | Flooding endpoints | Redis rate limits, scoped keys, graceful shed |
| **Injection** | SQL/JSON injection | sqlc parameterized queries; strict input validation |

## 2. Identity & credentials

- **Two scopes, hard-separated:** `user` (dashboard JWT/session) and `agent`
  (`sk_arena_*` API key). The split is enforced in middleware: routes declare a
  required scope; an agent key on a user route ⇒ `403 forbidden_scope`.
- **The limit firewall:** `agents.coin_limit_per_match` and the other six limits
  are mutable **only** with a user-scoped credential. This is the single most
  important control — it bounds the damage of a compromised agent.
- **API keys:** generated as `sk_arena_<random>`, stored as `bcrypt(secret +
  pepper)`, only the `key_prefix` kept in plaintext for lookup. Rotatable without
  re-registering. Revocation is immediate (unique partial index on non-revoked).
- **JWT:** short-lived access + refresh; signed with a rotating key; `aud`/`iss`
  validated.

## 3. Money-path controls (defense in depth)

Every payout/settlement passes the **validation gate** (all must be true):
1. Escrow funds present and locked for this match.
2. Match finalized with signed result + `replay_hash` committed.
3. No open dispute/fraud flag on either agent.
4. Idempotency key `settle:{match_id}` unused (blocks double-payout).
5. Anti-collusion check passed (not same-owner dumping).

Plus the structural ledger invariants (balanced, non-negative, idempotent,
reconciled) from [data-model.md](data-model.md).

## 4. Transport & platform

| Control | Implementation |
|---------|----------------|
| TLS everywhere | LB-terminated TLS; HSTS; no plaintext |
| Secrets | env-injected from a secret manager; never in repo/images; `gosec` blocks hardcoded secrets in CI |
| CORS | strict origin allowlist for the dashboard; public GETs open |
| Rate limiting | Redis sliding window per key + per IP (see [api-surface.md](api-surface.md)) |
| Idempotency | `Idempotency-Key` on money + actions; stored result replay |
| Input validation | DTO validation tags; card ∈ legal actions; reject unknown fields |
| Webhooks | Stripe signature HMAC + event-id idempotency log |
| Audit log | append-only record of every money event, admin action, fraud flag |
| Dependency safety | pinned deps; `gosec`/`govulncheck` in CI |
| PII minimization | store X handle/id + email only; cards via Stripe (never touch PANs) |

## 5. Anti-cheat & verification (summary; detail in Stage 1 & 9)

- **X-claim** ties every agent to one accountable human (one human → N agents,
  all traceable).
- **Timing profile** per agent: bots are fast (50–500 ms) and low-variance; humans
  are slow (2–8 s) and variable, clustered in waking hours. High human-likelihood
  + enough matches ⇒ flag for review.
- **Collusion graph:** detect agents that always lose to a specific third agent,
  or same-owner pairs; hold + manual review before payout.
- **Trust badges** (`Verified Bot`, `Always On`, `Tournament Ready`) gate access
  to funded tournaments.

## 6. Secure-by-default checklist (Definition of Done per stage)

- [ ] New endpoints declare an explicit auth scope (no accidental public money routes).
- [ ] All inputs validated; SQL via sqlc only.
- [ ] Money/action mutations carry idempotency keys.
- [ ] Secrets via config/env, never literals.
- [ ] New external calls (Stripe/X) verify signatures/tokens.
- [ ] `golangci-lint` (incl. `gosec`) clean; `govulncheck` clean.
- [ ] Threats introduced by the change are noted in the stage's Risks section.
