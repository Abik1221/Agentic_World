# Handoff — platform harness benchmark

State at the end of the session of 2026-08-15. Twelve commits across `Agentic_World` and
`Pyyol_client`, all verified locally, **none deployed** (GitHub Actions billing).

---

## What exists and is proven

| | commit | proof |
|---|---|---|
| `upstream_host` recorded at proxy time; only real-provider calls are attributable | `197efe5` | 228 real calls kept, 1,601 lab stand-in excluded; mutation-proven |
| Public sinks fail closed on agent kind (`kind = 'external'`, 32 sites) | `58b0145` | `TestPublicSinksUseAnAllowlist`, mutation-proven |
| `model_board_history` keyed `(board, day, model)`; empty board name refused | `58b0145` | `TestBoardHistoryKeepsBoardsIndependent`, mutation-proven |
| `harness` agent kind + separation guards | `895841a` | `TestHarnessAgentReachesNoPublicSink` |
| Harness board at `/v1/benchmark/harness` — second instance of the same fit | `02f8451` | serves; own worker, own snapshot |
| `gamelab` emits a scaffold fingerprint; per-seat providers | `5d95800` | Groq 19 / OpenRouter 19 bound in the same matches |
| `/v1/benchmark/harness/models` — thinking time, reasoning tokens, cost | `4331b84` | builds; `rating`/`httpx`/`store` green |
| `/harness` public page with logos, intervals, operational stats | client `0982b2a` | build + tsc clean |

**Design rule that must not be broken:** the developer board and the harness board run the
SAME fit over different matches. Two instances of `modelboard.Service`, sharing no mutable
state. Their SQL differs in exactly one CTE and everything after it is shared source. If you
find yourself copying a query, stop — that is how they drift.

---

## BLOCKER 1 — a harness run currently lands on the developer board

`gamelab` creates its agents as `kind='external'`. Verified:

```sql
SELECT a.kind, count(DISTINCT a.id) FROM agents a
  JOIN agent_model_calls mc ON mc.agent_id = a.id
 WHERE mc.upstream_host IN ('api.groq.com','openrouter.ai')
 GROUP BY a.kind;
-- external | 6
```

So running the batch today would put **platform benchmark agents on the public developer
leaderboard**, rated, as though they were developers — and leave `/harness` empty, because
no harness-kind seats exist for it to fit.

Everything downstream is correct. What is missing is the tap that routes a run into the
harness lane.

**Fix shape:** a `-harness` flag on `gamelab` that causes its onboarding to create agents
with `kind='harness'`. Agent creation goes through the API (`cmd/gamelab/api.go`), so this
needs the create-agent path to accept a kind — admin-scoped, never from an ordinary user
token. Reuse `auth.RequirePlatformOrAdmin`; do NOT invent a second auth path.

Do not shortcut this by UPDATE-ing the kind after the fact in SQL: matches already written
under `external` stay on the developer board, and you would be repairing rather than
preventing.

## BLOCKER 2 — the batch stalls at 4 matches

`max_concurrent_matches` is 1 per agent, so `-matches 14` produced 4 and then warned:

```
limit_max_concurrent_matches: Already in 1 active matches (limit 1)
```

Raise it for harness agents specifically (guardrails are per-agent). This is why every run
so far has been too small to rank.

---

## Phase 6 — Lens view for harness traffic (not started)

`tracing/` is a separate module on **Go 1.26** with its own stack. The arena already emits
spans through `telemetry.Client` (see `internal/llmgw` — the gateway emits per call).

What is needed: harness traffic distinguishable in Lens from developer traffic, so an
operator can trace a benchmark run without it mixing into user telemetry. The natural
discriminator is the same one the boards use — the agent's kind — carried on the span.

Nothing depends on this for correctness. It is observability.

---

## The run, once the blockers are cleared

Both keys were used from the environment only, never written to a file. **Rotate them** —
they were pasted into a chat session.

```bash
docker exec \
  -e PYYOL_PROVIDER_KEY_GROQ=... \
  -e PYYOL_PROVIDER_KEY_OPENROUTER=... \
  -e API_BASE=http://localhost:8080 -e AGENT_HOST=127.0.0.1 \
  pyyol-backend /src/.lab-gamelab \
    -game goofspiel -tier low -bind -harness -matches 40 \
    -bind-provider "groq,openrouter" \
    -bind-model "llama-3.1-8b-instant,google/gemma-4-26b-a4b-it:free"
```

Repeat for `-game mafia` and `-game monopoly`. **~30–50 matches per pairing per game** is
what it takes for an interval to exclude 50% — below that the board correctly refuses to
rank, which is the behaviour, not a bug.

Verify after:

```sql
-- every seat must be harness kind, and the calls must be externally served
SELECT a.kind, mc.upstream_host, count(*) FILTER (WHERE mc.bound)
  FROM agent_model_calls mc JOIN agents a ON a.id = mc.agent_id
 WHERE mc.created_at > now() - interval '2 hours' GROUP BY 1,2;

-- and the scaffold must be present, or nothing pairs and the board stays empty
SELECT count(*) FILTER (WHERE COALESCE(scaffold,'') <> ''), count(*)
  FROM agent_match_decisions WHERE created_at > now() - interval '2 hours';
```

Current standing, on a sample far too small to publish: **gemma-4-26b beat
llama-3.1-8b-instant 3–0**. Three games. A 3–0 sweep happens 1 time in 8 by chance.

---

## Before production

- **GitHub Actions billing** — every workflow fails in ~2s with
  *"recent account payments have failed or your spending limit needs to be increased"*.
  Nothing has deployed for the whole session.
- **SDK release PRs #57 (py 1.11.0) and #58 (js 1.11.0)** are open and mergeable; let
  release-please refresh them after these commits land so their changelogs are current.
- **PR #56** (Yabsira) is incorporated in `314eaf7` with co-authorship; close it with credit.
- **Migrations 0091–0094** need applying in production. 0092 means existing rows carry no
  provenance and are treated as unknown, so the developer model board shows unattributed
  until new calls accumulate. That is deliberate — see the commit message before
  "fixing" it.
- **The lab escrow does not reconcile** (1,513,500 coins with no story, almost certainly
  from the match purge). Pre-existing, fails at HEAD, and ledgers are never auto-repaired.
