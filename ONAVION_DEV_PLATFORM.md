# Onavion Beta Developer Platform — Architecture Ruling & Build Plan

_Design doc for the Beta Developer Platform (SDK + CLI + push protocol + docs)._
_Scope lock: **official SDKs for JS/TS + Python only**; the wire protocol stays
language-agnostic (any language can serve HTTP), but only those two SDKs are built
and documented for Beta._

---

## 1. The architectural ruling — HTTP vs event-driven (per interaction)

The spec's agent boundary is the **push model**: the platform calls the developer's
hosted HTTP server. The question is which interactions are HTTP request/response,
which are event-driven, and where (if anywhere) a persistent socket is warranted.

**Verdict: keep HTTP at the agent boundary; make the platform *internally*
event-driven; do NOT use WebSockets for agents.**

| Agent endpoint | Nature | Transport (correct) | Why |
|---|---|---|---|
| `GET /health` | liveness | HTTP req/resp | standard health probe |
| `POST /initialize` | match setup (seat/role/config) | HTTP req/resp | one per match; needs an ack |
| `POST /turn` | **decide a move** (engine blocks on it) | HTTP req/resp | REQUIRED synchronous — the engine needs the decision to advance (bounded by a timeout + deterministic fallback). No event-driven alternative fits a request-for-a-decision. |
| `POST /event` | notify: something happened (no action) | HTTP **webhook** (async) | one-way NOTIFICATION → the event-driven path. Delivered async off the internal event bus by a webhook dispatcher; never blocks the engine. |
| `POST /game-end` | notify: final result | HTTP **webhook** (async) | notification |

**Why NOT WebSocket/persistent connection for agents:**
- These are **turn-based** games; the developer's **LLM inference (seconds)**
  dominates latency — an HTTP round-trip (ms) is negligible. A socket buys ~nothing.
- The platform's *entire value prop* is **language-agnostic, developer-hosted,
  firewall-friendly**. A plain HTTP server is trivial in any language; a persistent,
  authenticated, reconnecting WebSocket client is much harder and provider-specific.
  A socket would undermine the core vision.
- `/turn` is inherently request/response — you cannot fire-and-forget a
  decision request.
- **Industry precedent:** Stripe, GitHub, Twilio, Shopify — REST for synchronous
  calls + **signed webhooks** for events. This *is* the industry-standard
  "event-driven over HTTP" pattern.

**Where event-driven is correct — and mostly already built:**
- **Internal fan-out**: match events → **transactional outbox → dispatcher**
  (already powers spectator SSE, ratings, the admin mirror, platform bus). The
  `/event` + `/game-end` webhooks MUST be dispatched from this same outbox by an
  async, retrying, at-least-once, HMAC-signed **webhook dispatcher** — so the engine
  never blocks and delivery is reliable + horizontally scalable.
- **Spectator → browser**: SSE (done).
- **Pull-agent wake-ups**: Redis notifier long-poll (done; Goofspiel + Mafia/Monopoly).

**Net:** the spec's HTTP endpoints are architecturally correct — keep them. The
"event-driven method" applies to `/event` + `/game-end` (async webhooks off the
outbox) and to all internal fan-out. No WebSockets at the agent boundary.

---

## 2. Current state vs spec (gap map)

**Already built (reuse, don't rebuild):**
- Manifest/registration pipeline (`internal/manifest`, `/v1/agents/{id}/manifest*`,
  submit → set-secret → verify → active) + a frontend registration UI (`/manifest`).
- A push primitive: `internal/agentclient` (SSRF-hardened `Health`/`Handshake`/`Play`)
  + `internal/remoteplay` (drives Goofspiel over the developer endpoint) + wired
  push-play for all 3 games (`/v1/{sandbox,monopoly,mafia}/pushplay`).
- Certification/validation + offline simulation: `internal/devplatform` (Certifier,
  GameSpec, sandbox), `cmd/certify`, `cmd/arena-sim`.
- Starter agents: `starter-agent/{python,go}` (currently PULL-model examples).
- Replay (append-only `match_events` + `/v1/{game}/{id}/replay`), spectator SSE.
- Internal event bus / transactional outbox (the foundation for webhook dispatch).

**Missing vs the Onavion Beta spec:**
1. **Full push lifecycle** — we have a single `/play`; the spec wants
   `/initialize` + `/turn` + `/event` + `/game-end`.
2. **Async webhook dispatcher** for `/event` + `/game-end` off the outbox (secure,
   retrying, HMAC-signed) — the event-driven delivery path.
3. **HMAC request signing + timestamp + replay protection** (we have a static
   bearer token + Ed25519 *move* signing, not the spec's per-request HMAC).
4. **Official SDKs (JS/TS + Python only)** — auth/HMAC, typed models, event parsing,
   response serialization, local simulation harness, config helpers. (No AI logic.)
5. **`onavion` CLI** — `init`, `login`, `run`, `simulate {mafia|monopoly|goofspiel}`,
   `validate`, `publish`.
6. **Docs set** — quick start → publish, targeting <30 min to first live game.

---

## 3. Phased build plan (each phase independently verifiable + committable)

- **P1 — Push-protocol contract + security (backend core).** Define the exact
  `/initialize` `/turn` `/event` `/game-end` schemas per game; extend `agentclient`
  to the full lifecycle; add HMAC-SHA256 request signing + timestamp + replay-nonce
  (verify helper for the SDKs). This is the contract everything else wraps.
- **P2 — Async webhook dispatcher.** Deliver `/event` + `/game-end` from the outbox
  via a retrying, at-least-once, signed dispatcher (health/latency/failure tracking;
  unhealthy endpoints skipped). Engine never blocks.
- **P3 — Drive the live match loop through the lifecycle.** Seat `remoteplay`-style
  deciders (now `/turn`) into the served match pipeline for all 3 games, so a
  registered push agent plays real matches (ranked + sandbox).
- **P4 — SDKs (JS/TS + Python). ✅ DONE.** Thin, model-agnostic, zero runtime
  deps: `Agent` server (routing + HMAC verify + replay guard + typed payloads +
  serialization), typed lifecycle + per-game models, and a local simulation
  harness (`simulate_goofspiel` / `LocalClient`). No AI logic; no provider
  lock-in. Verified: both SDKs' `computeSignature` is **byte-identical to the Go
  platform's `SignRequest`** (shared cross-language test vector), a full simulated
  match drives the whole lifecycle, and tampered/replayed requests are rejected.
  Living under `sdk/{python,js}`.
- **P5 — `onavion` CLI + local simulator.** `init/login/run/simulate/validate/publish`;
  offline baseline-agent matches (reuse `arena-sim`/`devplatform`), replay recording.
- **P6 — Validation pipeline + health monitoring polish** + **docs** (quick-start,
  auth, manifest, runtime API, game APIs, simulation, publishing, examples, errors, FAQ).

**Out of scope for Beta (per the spec):** hosted runtime, hosted memory, skill/agent
marketplaces, tournament simulator, prompt/memory inspectors, auto-optimization,
billing, plugin marketplace, cloud training. Also: SDKs for languages beyond JS/TS + Python.

**Security throughout:** server-authoritative engine (never trust agent responses —
validate action/target/resources/turn-order/rules); HMAC + timestamp + replay on the
agent boundary; per-turn timeout + deterministic fallback; SSRF-hardened outbound
client (already in `agentclient`).
