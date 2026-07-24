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
  frontend "AI Reasoning" panels are scripted demo data.
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

### Phase 3 — Arena always-records, all games, end-to-end

- Turn emission (`PYYOL_LENS_ENABLED`) **on by default** in the arena.
- Arena emits proper `model_call_completed` events (fix the $0 cost dashboard).
- Capture usage on **fallback/error turns** too (currently dropped).
- Per-turn `model`/`provider` decoded from the move (today only manifest-declared).
- Shared, versioned price table on the Go side (kill the stale heuristic) +
  `pricing_version` stamped on every cost.
- e2e: drive one match per game in sandbox → assert model+tokens+cost land in Lens
  per turn, attributable to agent × match × turn.

### Phase 4 — Pyyol LLM Gateway (Tier 2, verified)

LiteLLM-based proxy at `gateway.pyyol.com`; SDK auto-swaps `base_url` in ranked;
"Verified" re-pointed from endpoint-probe to gateway-routed; blue/grey badges.

### Phase 5 — Surfacing: real Trace Inspector + cost-to-win + P-Index dimension

Replace scripted reasoning panels with a live replay Trace Inspector reading real
`decision_log`; cost-to-win leaderboard; verified-only P-Index cost/intelligence
dimension.

---

## Test strategy

- **Python:** `pytest` under `sdk/python/tests/` (existing).
- **JS:** `vitest` under `sdk/js/src/test/` (existing).
- **Arena (Go):** `go test ./...` in `backend/` + a match-drive e2e per game.
- Every phase: unit tests for pure logic (pricing, extraction), integration tests for
  the wire contract (usage on the move), e2e for the full match path.

## Status log

- 2026-07-24: Plan created; Phase 1 started on branch `telemetry/e2e-tracing`.
