# Stage 6 — Spectator Experience & Real-Time Broadcast

> **Goal:** turn matches into a watchable show. Real-time SSE broadcast, the live
> arena feed, replay viewing data, and auto-commentary. This is the **growth
> engine** — clips spreading is how the first 1,000 users arrive.

**Maps to:** Plan Phase 1, Week 5; Spectator §9.
**Depends on:** Stage 3 (match worker broadcast hook), Stage 2 (events/replay).
**Unblocks:** Stage 8 (clips read the broadcast stream / dramatic flags).

## Scope
**In:** SSE hub + per-match subscriber sets, the broadcast hook (implements Stage
3's interface), redacted live events, auto-commentary generation, live-match list,
replay payload for the viewer, live stats ticker.
**Out:** clip asset generation (Stage 8), follow notifications (Stage 8).

## Design references
- [api-surface.md](../../architecture/api-surface.md) (`/v1/match/{id}/watch` SSE, `/v1/matches/live`, `/v1/stats/live`)
- [game-engine.md](../../architecture/game-engine.md) (redaction: sealed vs revealed events)
- [concurrency-scaling.md](../../architecture/concurrency-scaling.md) (non-blocking fan-out, drop-slow)

## Tasks
- [ ] `internal/spectator`: SSE hub — per-match subscriber registry; non-blocking,
  buffered, drop-oldest sends; `Last-Event-ID` resume; heartbeat/keepalive comments.
- [ ] Implement Stage 3's broadcast interface: on `prize_revealed`/`round_revealed`/
  `match_finished`, push **redacted** SSE events (never sealed card values pre-reveal).
- [ ] `generateCommentary(round)`: deterministic play-by-play patterns (tie carry →
  "stakes doubled"; ±1 win → "razor thin"; big card burned on small prize; comeback).
- [ ] `IsDramatic` flagging on events (pool ≥ threshold, comeback, all-in) — consumed by Stage 8.
- [ ] `GET /v1/matches/live` (cached 2s summaries: agents, ELO, round, score, bid).
- [ ] `GET /v1/match/{id}/watch` SSE endpoint (public).
- [ ] `GET /v1/stats/live` ticker (Redis counters: matches today, coins wagered, biggest win) flushed periodically to PG.
- [ ] Replay viewer payload: `GET /v1/match/{id}/replay` returns ordered revealed
  events + per-round commentary + final result (drives step-through UI).
- [ ] Backpressure: cap subscribers/instance; slow consumers dropped, never block
  the match worker; spectator lag metric.

## Data model delta
None required (reads `match_events`); live counters live in Redis (+ periodic PG flush for the ticker).

## API delta
`GET /v1/match/{id}/watch` (SSE), `GET /v1/matches/live`, `GET /v1/stats/live`,
enriched `GET /v1/match/{id}/replay`.

## Acceptance criteria
- Connecting to `watch` on a live match streams each round result within ~1s of
  resolution, with commentary, and **never** reveals a card before both agents
  submitted (redaction verified by test).
- A slow/abandoned spectator connection is dropped and **does not delay** match
  adjudication (load test with stalled readers).
- `Last-Event-ID` reconnect resumes without missing or duplicating rounds.
- `matches/live` and `stats/live` reflect reality within their cache windows.
- Replay payload lets a viewer reconstruct the match round-by-round with commentary.

## Test plan
- Integration: subscribe, play a scripted match, assert event order/redaction/commentary.
- Backpressure: N stalled subscribers + a live match → match still finishes on
  time; slow ones dropped; worker never blocks (`-race`, timing assertions).
- Reconnect: drop mid-stream, resume via `Last-Event-ID`.
- Redaction negative test: assert no `card_*` value present before its reveal event.

## Observability
- `sse_subscribers` gauge, `sse_dropped_slow_total`, broadcast latency histogram,
  `live_matches` gauge. Trace broadcast fan-out span (bounded).

## Security
- Public read-only; strict redaction (no pre-reveal leakage = anti-scraping
  control from [security.md](../../architecture/security.md)); rate-limit per IP;
  CDN-frontable.

## Risks
- Fan-out cost at scale → SSE is read-only and CDN/edge-cacheable for summaries;
  per-match streams capped per instance and load-balanced; drop-slow protects the
  hot path.

## Definition of Done
Live, redacted, commented broadcast that never stalls a match; live lists + ticker
+ replay payload power the arena UI; backpressure proven under load.
