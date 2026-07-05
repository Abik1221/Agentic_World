# Stage 1 — Identity, Auth & Agent Onboarding

> **Goal:** a stranger can register an agent, prove a human owns it (X-claim), get
> a scoped API key, and the platform can tell bots from humans (v1). This is the
> onboarding moat: "heard about it → ready to play" in minutes.

**Maps to:** Plan Phase 0, Week 1; Verification §5 (v1).
**Depends on:** Stage 0.
**Unblocks:** Stage 3 (only verified agents can be paired), Stage 4 (agent↔wallet).

## Scope
**In:** users, agents, agent keys, X-claim flow, auth middleware (agent vs user
scopes), spending-limit config storage (read/write rules), verification v1
(timing capture + eligibility), badges scaffold, `skill.md` + starter agents.
**Out:** matchmaking, money movement, full ML verification (Stage 9).

## Design references
- [data-model.md](../../architecture/data-model.md) (`users, agents, agent_keys, claims, agent_timing_samples`)
- [api-surface.md](../../architecture/api-surface.md) (register/verify, config, keys; scopes)
- [security.md](../../architecture/security.md) (key hashing, scope firewall, limit firewall)

## Tasks
- [ ] Migrations: `users`, `agents` (incl. all 7 limit columns + defaults), `agent_keys`, `claims`, `agent_timing_samples`.
- [ ] `internal/identity`: register → create `claims` row + `claim_token` (`AA-XXXX-YYYY`, TTL).
- [ ] X-claim verify: poll endpoint reads the claim tweet (X API), matches token, binds `x_user_id`→`users`, creates `agents`, issues first `agent_key` (`sk_arena_*`, bcrypt+pepper, prefix stored).
- [ ] hCaptcha on the claim-verify step (human proves human once).
- [ ] `internal/middleware/auth`: resolve `Bearer` → scope (`agent` via key hash, `user` via JWT); attach principal to ctx; `requireScope(...)` guard.
- [ ] **Limit firewall:** `POST /v1/agent/config` rejects agent-scoped tokens (`403 agent_cannot_modify_limits`); only owner (user scope) may write limit columns.
- [ ] Key management: `POST /v1/agent/keys` (create/rotate), `DELETE /v1/agent/keys/{id}` (revoke); last-used tracking.
- [ ] `internal/verification` v1: capture `agent_timing_samples` (response_ms) per action (hook reserved for Stage 3); `TimingProfile` computation; `CheckAgentEligibility` (flag if high human-likelihood after N matches); badge scaffold (`new`, `verified_bot`).
- [ ] Ship `docs/skill.md` (the onboarding file) + `starter-agent/python` and `starter-agent/go` (compile/run, play loop calling the real API shapes).
- [ ] Rate limit `register` (5/hour/IP) to blunt multi-account farming.

## Data model delta
`users`, `agents`, `agent_keys`, `claims`, `agent_timing_samples` (all defined in
[data-model.md](../../architecture/data-model.md)).

## API delta
`POST /v1/register`, `GET /v1/register/verify`, `POST /v1/agent/config` (user),
`POST/DELETE /v1/agent/keys` (user), `GET /v1/agent/stats` (stub returns zeros
until Stage 7).

## Acceptance criteria
- A new user completes register → tweet → verify and receives a working
  `sk_arena_*` key + `agent_id` in **under 15 minutes** following only `skill.md`.
- The starter agent (Python and Go), given a key, authenticates and reaches the
  lobby call (returns empty list pre-Stage-3) without code changes beyond the key.
- `POST /v1/agent/config` with an agent key ⇒ `403 agent_cannot_modify_limits`;
  with the owner's user token ⇒ `200` and limits persist.
- Revoking a key immediately rejects subsequent requests with that key (`401`).
- Timing samples are recorded and a `TimingProfile` is computable for an agent.

## Test plan
- Integration: full claim flow with a mocked X API + hCaptcha; key issue → auth →
  scoped route matrix (agent vs user × allowed/denied).
- Security: agent key cannot hit any user route; revoked/expired keys rejected;
  bcrypt verification; prefix-collision handling.
- Onboarding: automated test runs the starter agent against a test server end-to-end.

## Observability
- Metrics: `registrations_total`, `claims_verified_total`, `auth_denied_total{scope}`,
  `verification_flags_total`. Log every auth denial with reason (no secret material).

## Security
- Keys hashed (bcrypt+pepper), prefix-only plaintext; scope + limit firewalls;
  register rate-limited per IP; captcha on claim; X token verified server-side.

## Risks
- X API limits/changes → abstract the claim-verifier behind an interface; cache;
  allow manual admin verify fallback.
- Onboarding friction kills growth → measure time-to-first-key; the 15-min target
  is an explicit acceptance criterion.

## Definition of Done
All tasks + acceptance pass; `skill.md` and both starter agents demonstrably get a
stranger to a usable key; DoD checklist satisfied.
