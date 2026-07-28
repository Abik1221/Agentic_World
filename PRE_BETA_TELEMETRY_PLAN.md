# Pre-Beta Telemetry, Anti-Cheat & Verification Plan

Goal: **trustworthy, evidence-based per-activity tracing — the exact model, token
counts, and cost for every agent turn — end-to-end, across sandbox AND ranked, for
ALL games (Goofspiel, Mafia, Monopoly, + future).** This is the foundation for
honest benchmarks, the "Agentic LMSYS" model leaderboard, cost-to-win, and a
credible P-Index.

This plan is derived from the pre-beta audit (see conversation) and the
`pyyol-telemetry.md` architecture doc. We ship **one phase at a time**, each with
unit + integration/e2e tests green before moving on.

---

## Where we are today (audit findings)

- Model/token/cost are **self-declared** by the agent in an optional `usage` field;
  the arena engine makes no LLM calls and cannot observe real usage.
- The SDK tracer (`pyyol.Tracer`) is solid but **fully manual** — the dev must call
  `span.log_model_call(...)`. Nobody does.
- Lens emission is **off by default**; the arena emits **no `model_call_*` events**,
  so the cost dashboard shows **$0**.
- The price table is a **stale heuristic** with no version and no Opus/Sonnet split.
- No **LLM gateway/proxy** exists → model identity is unverifiable ("cannot verify a
  remote API's model" — `manifest/schema.go:65`).
- "Verified/Certified" gates on an **endpoint health probe**, not model provenance.
- Reasoning (`rationale`) is captured optionally but **shown nowhere** — the
  frontend "AI Reasoning" panels no longer fabricate anything: synthesized reasoning,
  confidence and ROI are demo-gated, so on a live match they render the agent's own
  words or hide entirely. They stay EMPTY until the chat/rationale pipeline is traced.
- P-Index deliberately **excludes** model/tokens/cost (correct while untrusted).

---

## Architecture: two tiers (resolves "invisible" vs "verified")

- **Tier 1 — Sandbox / Casual (grey "Unverified").** The SDK *auto-instruments* the
  dev's LLM client (OpenAI, Anthropic, …) and captures real model + tokens + cost
  with zero dev code, attaching it to every move. Low friction, labeled *estimated*.
- **Tier 2 — Ranked / Verified (blue "Verified").** In ranked mode the SDK
  transparently points the dev's LLM `base_url` at the **Pyyol Gateway** (LiteLLM
  router). Unfakeable model/token/latency. This *replaces the endpoint-probe* as the
  trust signal for ranked. Unlocks cost-to-win + a verified P-Index dimension.

Both tiers feed the SAME telemetry pipeline (`match_<id>` trace id) so sandbox and
ranked traces render identically.

---

## Phased delivery (one thing at a time)

### Phase 1 — SDK auto-capture (Tier 1 core)  ← DONE (Python)

Make the SDK capture real model + tokens + cost with no dev effort and attach it to
every move, for all games, sandbox and ranked. Independent of Lens being enabled
(usage rides the move to the arena regardless).

- [x] 1a. `pyyol/pricing.py` — versioned price table (`PRICING_VERSION`) +
        `estimate_cost()`; distinct Opus/Sonnet/Haiku; cached/reasoning tokens.
- [x] 1b. Turn-local usage accumulator in `telemetry.py` (`UsageAccumulator`,
        `turn_usage()`, `current_usage()`) — contextvar, always-on.
- [x] 1c. `pyyol/instrument.py` — `instrument()` auto-wraps OpenAI + Anthropic
        (chat, messages, Responses API; sync + async); pure `extract_usage()`;
        computes cost, logs a `model_call` span, accumulates; `uninstrument()`.
- [x] 1d. Auto-attach accumulated `usage` to the outgoing move in
        `runtime._handle_turn` — dev-supplied `usage` always wins; no code required.
- [x] 1e. Unit tests (`tests/test_pricing.py`, `tests/test_instrument.py`): pricing,
        extraction from OpenAI/Anthropic/Responses/dict, cost math, accumulation.
- [x] 1f. Integration tests: instrumented fake client through a real runtime turn →
        move on the wire carries correct `usage`; dev-supplied precedence; no-call case.
- All 93 Python SDK tests green; ruff clean; `import pyyol` stays cheap (no eager
  provider import).

### Phase 2 — JS SDK parity  ← DONE (capture); CLI parity deferred

Mirror Phase 1 in `sdk/js`.

- [x] `pricing.ts` — versioned table + `estimateCost()` (parity with Python).
- [x] `telemetry.ts` — `UsageAccumulator`, `runTurnUsage()`, `currentUsage()` via a
      second `AsyncLocalStorage` (always-on).
- [x] `instrument.ts` — `instrument()`/`uninstrument()`, `extractUsage()`,
      `recordResponse()`, `patchPrototype()`; wraps OpenAI (chat + Responses) and
      Anthropic messages; guarded + idempotent.
- [x] `runtime.ts` — turn runs inside `runTurnUsage`; usage auto-attached to the
      move (dev-supplied wins); exports wired in `index.ts`.
- [x] Tests (`src/test/pricing.test.ts`, `src/test/instrument.test.ts`): unit +
      integration/e2e through the runtime. Full JS suite 77 passing; prod build clean.
- [ ] Deferred: add JS `wallet`/`queue` CLI commands (parity gap; not telemetry).

### Phase 3 — Arena always-records, all games, end-to-end  ← DONE (core)

- [x] Turn emission on by default: `PYYOL_LENS_ENABLED` defaults `true`
      (`config.go`) — still gated by endpoint+key, so unconfigured = silent no-op.
- [x] Shared, versioned Go price table `internal/pricing` (mirrors the SDK) +
      `pricing.Version`; kills the stale heuristic, distinct Opus/Sonnet/Haiku.
- [x] Cost computed server-side: `SeatSummary.EstimatedCost` accumulated per move in
      `benchmark.Record` (uncapped), `pricing_version` stamped; surfaced on
      `benchmark_recorded`.
- [x] Arena emits real `model_call_completed` events (per move with usage) — the
      event the cost-analytics query filters on, so the **$0 dashboard is fixed**.
      High-priority (never sampled away). Works for ALL games (shared benchmark path).
- [x] Per-turn REAL `model`/`provider`/`cached_tokens` decoded from the move
      (`TokenUsage` extended); flows through Goofspiel/Mafia/Monopoly unchanged since
      all decode `*benchmark.TokenUsage`. Falls back to the manifest model.
- [x] Tests: `internal/pricing/pricing_test.go` + `internal/benchmark/emit_test.go`
      (capturing-emitter integration): asserts per-turn model_call events, per-move
      cost, seat cost, pricing_version, correlation. `go build ./...` + affected
      tests green; gofmt/vet clean.
- [ ] Deferred: full match-drive e2e per game (needs a Postgres+socket harness) —
      covered at the emission layer for now via the shared path.
- [ ] Inherent limit: genuine no-response turns (timeout/transport/disconnect) carry
      no usage — the agent sent nothing to measure. The Pyyol Gateway (Phase 4) is
      the real fix; illegal-move turns that DID return a move already flow usage through.

### Phase 4 — Pyyol LLM Gateway (Tier 2, verified)  ← IN PROGRESS

The unfakeable counterpart to Tier 1: route ranked LLM traffic through a Pyyol
proxy so model/tokens/cost are server-observed, not self-declared.

- [x] 4a. Gateway proxy core `internal/llmgateway` — transparent reverse proxy
      (forwards OpenAI `/openai/v1/*` + Anthropic `/anthropic/*` verbatim, dev's own
      provider key passed upstream, Pyyol control headers stripped); observes the real
      response, prices via `internal/pricing`, emits a **server-verified**
      `model_call_completed` (`verified:true`, `source:gateway`) attributed to
      agent × match × turn. Streaming passes through; non-2xx never bills. Tests
      (`gateway_test.go`, fake upstream + capturing emitter): forwarding, transparency,
      auth-required, per-provider usage/cost, streaming, error paths. build/fmt/vet clean.
- [x] 4b. Wired into the server: `PYYOL_LLM_GATEWAY_ENABLED` config (default off),
      mounted at `/gw/*` (`cmd/server/llmgateway.go`), store-backed Authenticator
      validating the Pyyol agent key via `idSvc.ResolveAgentKey` (ScopeAgent). Full
      `go build ./...` + tests green.
- [x] 4c. SDK gateway routing (Python + JS): `enable_gateway()`/`route(client)` +
      per-turn `X-Pyyol-Key`/`Match`/`Turn` header injection into instrumented calls
      (Python `extra_headers`, JS request-options `headers`); turn context (match/turn)
      threaded through `turn_usage`/`runTurnUsage` from the runtime. Tests:
      `tests/test_gateway.py` (10) + `src/test/gateway.test.ts` (9); full suites green
      (Py 103, JS 86); ruff/prod-build clean.
- [x] 4c-wire. CLI auto-enables gateway routing in ranked mode (Python + JS):
      `DEFAULT_GATEWAY` (env `PYYOL_GATEWAY`, default `https://gateway.pyyol.com`);
      `enable_gateway(agentKey, url)` fires on ranked when the connection token is an
      agent key. Dev routes with one line: `pyyol.route(client)`. Suites green
      (Py 103, JS 86); ruff/prod-build clean.
- [x] 4d. Dual-badge model (chosen over strict re-point): endpoint-probe certification
      still gates ranked entry; a SEPARATE "Verified" badge is awarded when an agent's
      LLM traffic is observed through the gateway — a bonus signal, no new friction.
      Backend: `agent.gateway_verified` event + `GatewayVerified` badge + handler
      (`badges.OnAgentGatewayVerified`); gateway `WithVerifiedHook` fires on the first
      observed call per agent (Lens-independent); main dedups in-process + emits the
      event; badge surfaces on the agent profile. Tests (gateway hook, badge handler)
      green; build/fmt/vet clean.
- [ ] 4d-ui. Frontend cosmetic: map the `gateway_verified` code to a blue "Verified"
      chip in the agent-profile badge renderer (separate client repo).

### Phase 5 — Structural trace data + surfacing  ← IN PROGRESS

Guiding principle: everything lands in Pyyol Lens as FIRST-CLASS, queryable columns
(not JSON blobs), so the backend can compute anything (cost-to-win, P-Index cost
dimension, dashboards) straight from the trace store.

- [x] 5a. Structural economics on every emitted event. Arena `telemetry.Event`
      gains `cached_tokens`, `reasoning_tokens`, `pricing_version`, `currency`, and
      `meter_source` — mapping 1:1 onto the Lens ClickHouse columns that already
      exist. `meter_source` = `gateway` (server-observed, unfakeable) vs `sdk`
      (self-reported) is the STRUCTURAL verified signal. Set on `benchmark_recorded`,
      per-turn `model_call_completed`, and gateway calls. Tests green; build/fmt/vet clean.
- [x] 5b. Cost-to-win + lifetime cost on the profile. Promoted `result` to a
      structural column (`0056_agent_match_benchmark_cost`: adds `game`,
      `estimated_cost`, `result`), fed from the same `match.benchmark` fact
      (`RecordMatchBenchmark`). New `profiles.Economics` on the profile: lifetime
      `games`, `wins`, `total_cost_usd` (overall + per-game), and `cost_per_win_usd`
      (headline + per game), aggregated by `ProfilesRepo.Economics` and folded by the
      pure, unit-tested `CostPerWin`/`foldEconomics`. Build/vet/gofmt + tests green.
      (Lens-side cost-to-win analytics query can reuse the structural columns later.)
- [x] 5c. Verified cost pipeline + cost-efficiency primitive. Gateway now records
      per-(match,agent) GATEWAY-VERIFIED cost into Postgres (`0057_agent_match_verified_cost`
      + `RecordVerifiedCost`; hook fires per call with match+cost, cost accrues every
      call, badge still once). Pure `pindex.CostEfficiencyScore` (verified cost-per-win
      → 0..scale, un-gameable — gateway data only) unit-tested. Build/vet/gofmt + tests green.
      NOTE: wiring it into the composite P-Index (config version + reweight) is a
      deliberate live-DB-validated toggle, mirroring the Intelligence rollout — not flipped here.
- [ ] 5d. Real Trace Inspector UI — replace the scripted reasoning panels with a live
      replay reading the real `decision_log` (per-turn action/rationale/model/tokens/cost).

---

## Test strategy

- **Python:** `pytest` under `sdk/python/tests/` (existing).
- **JS:** `vitest` under `sdk/js/src/test/` (existing).
- **Arena (Go):** `go test ./...` in `backend/` + a match-drive e2e per game.
- Every phase: unit tests for pure logic (pricing, extraction), integration tests for
  the wire contract (usage on the move), e2e for the full match path.

## Status log

- 2026-07-24: Plan created; Phase 1 started on branch `telemetry/e2e-tracing`.
