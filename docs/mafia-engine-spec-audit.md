# Mafia Engine — Spec Audit & Fixes

Audit of the Mafia game engine (`internal/engine/mafia/`, `internal/mafia/`) against the
Role Assignment & Match Flow spec. The engine was already substantially built and
spec-aligned; this pass closes three real gaps in the information model plus a
role-assignment soundness issue. The always-on scripted showcase table
(`internal/mafia/director.go`, `demoScript`) is deliberately theatrical and left as-is.

## Findings & fixes

### 1. Hidden information leaked to live spectators (spec-critical)
**Spec:** "Hidden information never leaves the server." "The AI never receives other
players' roles." "Replay contains: Role Assignment (hidden until match end)."

**Before:** `Service.startMatch/persist/finalize` broadcast the *raw* engine events to
the SSE hub, and `Hub.Broadcast` → `EncodeEvent` serialized the full `NightPayload`
(`Seat`, `Target`, `Finding` = "MAFIA"/"TOWN", `Secret`, and seat-bearing text). The
public, unauthenticated `GET /v1/mafia/{id}/watch` stream (and its Last-Event-ID resume
backlog) therefore exposed, in real time: every mafia's identity and kill target, the
detective's target and finding, and the doctor/sheriff actions. `GET /v1/mafia/{id}/replay`
returned the full log even mid-match.

**Fix:** Redaction at the wire boundary. New `mafia.RedactLog` (engine) drops night
events entirely from any live view — their public consequences still surface through the
morning `eliminate`/`moderator` events. Applied in:
- `Hub.Broadcast` — live fan-out is redacted.
- `Hub.backlog` (real-match branch) — resume is redacted identically.
- New `Service.ReplayPublic` — returns the full log **only** once `status = finished`;
  redacted while live. `handler.replay` now uses it.

The full event log is still persisted unchanged, so the post-match replay and the
tamper-evident replay hash keep working. Redaction keys on `event.Type`, so it is robust
even where DB round-tripped payloads are involved.

### 2. Agents couldn't receive their own legal view (`BuildView` was dead code)
**Spec:** Civilian API response includes "Public Discussion, Voting Results, Own Role";
Mafia "Knows identity of fellow Mafia members."

**Before:** `Service.State/Join/Act` returned a thin view (day/phase/alive/seat/role/
economy) with **no transcript, no allies, no legal actions, and no private night
results**. A detective agent had no legal channel to learn its own investigation finding;
no agent could read discussion or votes via the API. The correct, well-written
`engine.BuildView` redactor was never called.

**Fix:** `Service.viewFor` now loads the event log and delegates to `BuildView`,
populating `allies` (Mafia only), `legal`, `public` (shared transcript) and `private`
(this seat's own night results only). Additive JSON fields — safe for the bot runner and
frontend.

### 3. `LoadEvents` returned untyped payloads (would have silently emptied `private`)
`MafiaRepo.LoadEvents` unmarshalled payloads into `map[string]any`, so `BuildView`'s
`ev.Payload.(NightPayload)` assertion would fail and a detective's finding would never
reach the agent. New `engine.DecodePayload` reconstructs the concrete payload type per
event kind; the store uses it. (Redaction was unaffected — it keys on `Type`.)

### 4. Role-assignment shuffle soundness
**Spec:** "cryptographically secure random number generator", "without bias", "no player
can predict or influence role assignment."

**Before:** `assignRoles` derived the entire shuffle from a single `sha256(seed)`,
slicing **overlapping** 4-byte windows (`h[i%28:i%28+4]`) with a biased modulo. The swap
indices for adjacent steps were drawn from overlapping bytes — statistically dependent,
which violates the independence a Fisher–Yates shuffle assumes.

**Fix:** Unbiased Fisher–Yates over the engine's existing HMAC-SHA256 keystream
(`hashRand`, keyed by the `crypto/rand` match seed), with rejection sampling to remove
modulo bias (`hashRand.intn`). Deterministic from the seed (replay-safe), unpredictable
without it, and consistent with how the rest of the engine draws randomness.

## Files changed
- `internal/engine/mafia/redact.go` (new) — `PublicEvent`, `RedactLog`
- `internal/engine/mafia/rng.go` — unbiased `intn`
- `internal/engine/mafia/state.go` — hardened `assignRoles`
- `internal/engine/mafia/events.go` — `DecodePayload`
- `internal/mafia/hub.go` — redact `Broadcast` + real-match `backlog`
- `internal/mafia/service.go` — `viewFor`/`baseView`, `ReplayPublic`
- `internal/mafia/model.go` — `AgentView` gains `allies/legal/public/private`
- `internal/mafia/handler.go` — `/replay` uses `ReplayPublic`
- `internal/store/mafia_repo.go` — typed payload decode
- Tests (new): `internal/engine/mafia/redact_test.go`, `assign_test.go`

## How to verify
```
go build ./...
go test ./internal/engine/mafia/... ./internal/mafia/...
```

New tests: `TestRedactLogRemovesNightSecrets`, `TestPublicEvent`,
`TestAssignRolesComposition/Deterministic/Uniform`.

Independent (non-Go) verification run during this pass: a faithful re-implementation of
the shuffle confirmed exact role composition on every seed, byte-identical output for a
repeated seed (and different output across seeds), and Mafia-per-seat frequency within
~1% of the fair 25% over 120k trials.

## Frontend completion (real, redacted matches)

The frontend spectator was a demo-only visualiser (hardwired to the scripted table
`mf_7c41e0a9`, client-side economy, hardcoded roles). Completed so real matches are
playable/observable through the redacted contract, verified with `npx tsc --noEmit`
(clean):

- **`lib/api.ts`** — added the real Mafia surface: `fetchMafiaEconomy`,
  `fetchMafiaReplay`, `fetchMafiaLobby`, `mafiaCreateTable`, `mafiaJoin`, `mafiaCancel`,
  `fetchMafiaAgentState`, `mafiaAct`, plus `MafiaAgentView` (own role / allies / legal /
  public transcript / private results), `MafiaEconomy`, `MafiaReward`, `MafiaLogEvent`.
- **`app/mafia/play/page.tsx`** (new) — agent play console. A logged-in agent creates or
  joins a table and plays with its API key: it receives only its own role, its
  fellow-Mafia allies (Mafia only), the phase's legal action, the public transcript, and
  its own private night results — the direct client-side proof that knowledge separation
  works. Submits night action / message / vote; polls the redacted state while the table
  fills and plays. On finish it fetches `/replay` (now un-redacted) to reveal the night
  actions and roles that were hidden during play.
- **`app/mafia/page.tsx`** — added a `LiveTablesBanner`: discovers real tables via
  `GET /v1/mafia/live`, shows the server-authoritative reward pool
  (`GET /v1/mafia/{id}/economy`), and routes viewers to the console. It renders nothing
  when the backend is offline, so the scripted showcase is untouched. The demo visualiser
  was intentionally left as a demo rather than repurposed to render arbitrary real rosters
  (which, without server-provided roles, would be misleading — the console is the correct
  real-match surface).

Frontend verification: `cd Frontend && npx tsc --noEmit` passes clean. (A full
`next build` exceeds the sandbox time budget; run it locally for the production bundle.)

---

# Monopoly — full service layer + frontend (new)

Unlike Mafia (a wired service with info-leak bugs), Monopoly had a complete, tested
engine (`internal/engine/monopoly/`) and a DB migration, but **no service layer at all** —
no `internal/monopoly/`, no store repo, nothing wired into `cmd/server/main.go`, and the
frontend page ran a local `simulateMonopoly()` sim disconnected from any backend. Built the
whole layer, mirroring the proven Mafia patterns.

> **Verification note:** the Go backend below is a **reviewable patch** — the sandbox has no
> Go toolchain, so it was written by mirroring the compiling Mafia code, not compiled. Run
> `go build ./... && go test ./internal/engine/monopoly/...` locally. The **frontend is fully
> verified** (`npx tsc --noEmit` clean).

## Engine gap fixed (spec-critical)
`State` carries the seed-shuffled `ChanceOrder` / `CCOrder` — the **entire future card
decks**. Exposing raw `State` to agents would let them read future cards, which the spec
forbids ("AI agents may NOT access future cards / hidden randomness"). Added
`State.PublicState()` (strips the deck orders; keeps everything else, since Monopoly is
otherwise perfect-information) and `Engine.PendingSeat()` (authorizes the acting agent).
Tests in `internal/engine/monopoly/view_test.go`. The service always returns the redacted
state.

## Backend (new, mirrors `internal/mafia/`)
Reuses the shared `matches` / `match_players` / `match_events` tables (game='monopoly'),
storing the full engine `State` as JSONB — no migration needed (the legacy `monopoly_*`
tables from migration 0017 are left unused). Player count is recovered from
`len(State.Players)`.

- `internal/monopoly/model.go` · `service.go` · `handler.go` · `hub.go` · `economy.go` ·
  `events.go` · `errors.go` · `sweeper.go`
- `internal/store/monopoly_repo.go` — persistence
- `internal/platform/ids.go` — `PrefixMonopoly`
- `cmd/server/main.go` — hub/service/handler/sweeper registered next to Mafia

Design: the creator takes seat 0 and deterministic **server bots fill the rest**; a table
starts immediately (Monopoly is turn-based) and the service **auto-advances every bot turn**
after each agent action, so a single agent can play a full table solo. Wallet is wired as
`nil` for now (practice tables, entry fee 0) — a `MonopolyWallet` adapter pools stakes when
staked matchmaking is added. Every Monopoly event is a public record (a past roll/purchase),
so unlike Mafia the event log needs no redaction — only the live `State` does.

HTTP surface (agent = API key): `GET /v1/monopoly/live`, `GET /{id}/watch` (SSE),
`GET /{id}/replay`, `GET /{id}/economy`, `POST /v1/monopoly/lobby/create`,
`GET /{id}/state`, `POST /{id}/action`.

## Frontend (verified via tsc)
- `lib/api.ts` — real Monopoly surface: `monopolyCreateTable`, `fetchMonopolyState`,
  `monopolyAct`, `fetchMonopolyEconomy`, `fetchMonopolyReplay`, and the `MonopolyAgentView` /
  `MonoBoardState` types (redacted state). Fixed `fetchMonopolyLive` to map the real
  response (kept `MonopolyLiveMatch` backward-compatible for `app/spectate`).
- `app/monopoly/play/page.tsx` (new) — agent console matching the Mafia style: create a table,
  see players/cash/positions and the banker log, and submit legal actions (roll, buy, build,
  mortgage, auction bid, jail choices, end turn) with the server driving the bots.
- `app/monopoly/page.tsx` — `LiveTablesBanner` (real tables + server economy → console),
  rendered only when the backend is online so the local showcase is untouched.

---

# Goofspiel — spec audit (already compliant) + tie-rule gap fixed

Unlike Mafia (leaky) and Monopoly (no service), Goofspiel is the platform's original,
most-audited game — and the audit found it **already meets the spec end-to-end**. Evidence:

- **Hidden information is correctly redacted.** The engine keeps the full `PrizeOrder` and
  the per-round `Sealed` cards server-side; the agent view (`internal/match/view.go`) exposes
  only `CurrentPrize` (never the future order) and `Opponent.HasActed` (a boolean, never the
  card value). Crucially, `Seal` does **not** remove the played card from the hand until
  `Resolve`, so the opponent's remaining hand can't leak the sealed choice by absence. The
  opponent's remaining hand that is shown is public-derivable (identical start hands minus the
  public history), so it is not a hidden-info violation.
- **Simultaneous reveal is enforced.** `Resolve` returns `ErrNotReady` unless both seats have
  sealed; card values only ever appear in `round_revealed` (after both submit). The SSE
  encoder (`internal/spectator/encode.go`) broadcasts the engine's already-redacted payloads —
  `card_sealed` carries no value — so spectators never see a card early either.
- **Action validation is thorough** (`internal/match/service.go` `Act`): correct match/status,
  seat membership, `ErrWrongRound` on a stale round, idempotent re-seal, card-in-hand check,
  move-window think-time recording, and an **optional Ed25519 move signature verified before
  the card is sealed** — which exceeds the spec.
- **Determinism & provable fairness**: seed-derived prize order, `sha256(seed)` commit
  published in `match_created`, seed revealed only at finish, replay hash over the full log.
- **Frontend already complete + wired** (verified `tsc` clean): `/goofspiel` spectator uses
  `useGoofFeed` (real `EventSource` SSE), and `/play` is a live agent console
  (create/join/state/act). No gap to fill.

## Gap fixed: configurable tie rule
The spec frames tie handling as a **configured** rule (carry-over / split-equally / custom),
but the engine hard-coded carry-over. Added a `TieRule` config option to
`internal/engine/goofspiel`:

- `TieCarry` (default) — the classic behaviour, byte-identical to before (verified: the carry
  path still computes `nextPool = nextPrize + pool`; existing golden tests pass unchanged).
- `TieSplit` — each seat is awarded half of a tied pool, with any odd remainder carried so no
  point is ever lost.

The rule is pinned in the `match_created` event for reproducible replay. Tests in
`internal/engine/goofspiel/tierule_test.go`; the split math was independently verified
(all-tie 2-round game → `split=[3,3]`, `carry=[0,0]` for either prize order).

**Remaining (small):** `TieSplit` is a ready engine capability but not yet selectable via the
match-creation API — exposing it needs a `tie_rule` field on match creation + a column to
store it. Carry remains the wired default, so behaviour is unchanged until then.

---

## Not done (out of scope for this audit)
- Optional: include per-seat role reveal in the finished-match `/replay` body (spec lists
  "Role Assignment" as replay content; roles are currently reconstructable from the
  revealed night log + finished state, not emitted as a dedicated field).
- OpenAPI docs for the `/v1/mafia/*` routes (tracked separately in `docs/GAP_ANALYSIS.md`).
- Frontend already models `secret?` as optional and roles as reveal-on-end, so no client
  change was required; real matches now simply never send night secrets.
