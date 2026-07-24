# SDK, llms.txt & Docs — Audit + Fix Plan

A 10x-engineer teardown found the SDK/docs actively hide the good paths, teach wrong
ones, and fail silently. Verdict: the engine is good; the developer surface isn't.
This plan tracks the fixes. We build the **docs-as-data** system first (accurate,
modular, versioned, served to the frontend), then work down the other findings.

## Audit headline findings (severity)

- 🔴 The flagship **verified telemetry** (`instrument()`/`route()`/gateway) is invisible
  in every README/example/doc; `quickstart.md` even lists it as "not yet available"
  while the CLI runs it.
- 🔴 Docs are stale/wrong vs code: `pyyol queue --game` (is positional), bare
  `pyyol publish` (needs `--manifest`), `simulation.md` "no WebSocket / plain HTTP"
  (opposite of the architecture), wrong auth model, dead hosts (`pyyol.example`,
  `docs.pyyol.com`).
- 🔴 Silent failures: JS swallows handler exceptions → generic `handler_error`; gateway
  routing silently no-ops without an agent key; sandbox retry gives up with no output.
- 🔴 Two rival everything: two agent APIs, two "start here" docs, two contradictory
  `llms.txt` files.
- 🟠 Untyped JS API (`step(view: unknown)`); no async `step` in Python (500s); toy
  examples (no LLM); JS missing `wallet`/`queue`; Goofspiel-only `simulate`;
  inconsistent view schema (`seat`/`your_seat`, `legal_actions`/`legal`).
- 🐛 Code bug: `runtime` sets `turn = view.round`, which is `0` for Mafia (`day`) and
  Monopoly (`phase`) → `X-Pyyol-Turn` always 0 for 2/3 games.

## Fix plan

### D1 — Docs-as-data engine + accurate content  ← DONE (backend)

Modular Markdown source of truth → seeded into a versioned DB table → public API for
the frontend docs UI.

- [x] `internal/docs`: content model + frontmatter parser (`docs.go`, pure/tested) +
      embedded modular content under `content/`.
- [x] Accurate content rewrite (fixes the audit lies): `getting-started/*` (welcome,
      install, first-agent), `sdk/*` (agent-api, **verified-telemetry**, cli-reference,
      testing-locally), `games/{goofspiel,mafia,monopoly}` (real view/move schema +
      code examples), `ranked/index`.
- [x] `migration 0058_docs_pages` + `store.DocsRepo` (seed/list/get/versions),
      startup seed at `docs.DocsVersion`, idempotent.
- [x] Public API `internal/docs/handler.go`: `GET /v1/docs` (nav tree), `/v1/docs/*`
      (page, nested slug), `/v1/docs/versions`. Tests: parser + handler green;
      build/vet/gofmt clean.
- [ ] D1-ui: frontend docs UI (separate `Pyyol_client` repo) — render the tree +
      Markdown, with a version selector; link "Docs" in nav.
- [ ] D1-more: author the remaining pages (protocol/local-runtime, manifest, wallet,
      P-Index, per-game deep dives) into the same content dir.
- [ ] D1-admin: admin CRUD to edit/override a page version without redeploy.

### D2 — Fix the SDK docs/examples in-repo  ← IN PROGRESS
- [x] Real LLM example (Py+JS) using `instrument()`+`route()`: `examples/llm_agent.py`,
      `examples/llm-agent.ts`.
- [x] De-stale the dangerous docs: `quickstart.md` (deleted the "not yet available"
      telemetry lie → real "Verified LLM agents" section; fixed `publish --manifest`,
      PAT→`sk_arena_`, login providers); `simulation.md` (fixed "no WebSocket/plain
      HTTP" FAQ → real outbound-WS + in-process-simulate + legacy-HTTP); `ranked.md`
      (`queue <game>` positional, `publish --manifest`, Python-only note).
- [x] `gen_llms.py`: host `docs.pyyol.com` → `pyyol.com/docs`; added `quickstart.md`
      + new `verified-telemetry.md` to the corpus; regenerated `llms.txt`/`llms-full.txt`
      + bundled rules. New `sdk/docs/verified-telemetry.md` authored.
- [x] `local-runtime.md`: fixed auth (endpoint-secret → `sk_arena_` login key),
      `pyyol.example` → real hosts (`api.pyyol.com`), `pyyol run`→`pyyol dev` primary,
      `simulate --game`.
- [x] Both READMEs (Python + JS): added a "Verified LLM agents" section (the flagship
      that was absent) with the two-line `instrument()`/`route()` snippet + example link.
- [ ] Remaining: README command-list completeness + `onavion` CHANGELOG; reconcile
      `Pyyol_client/public/llms.txt` (separate frontend repo) + CI drift-gate it;
      wire the LLM example into `pyyol init` (SDK code — D3/later pass).

### D3 — SDK code fixes (the "SDK later" pass the user will push)
- [ ] Stop swallowing handler errors (surface in `dev`, Py+JS).
- [ ] Make gateway-no-op loud; fix `turn=` attribution (per-game turn key).
- [ ] Type the JS API (`Adapter<View,Move>`); async `step` in Python; unify view schema.
- [ ] JS `wallet`/`queue`; `simulate` for all 3 games; footgun move defaults.

## Notes
- No Postgres integration-test harness in-repo — new store SQL is compile/vet-checked,
  pure logic unit-tested.
