# Stage 8 — Engagement: Clips, Follows & Notifications

> **Goal:** manufacture and distribute virality. Auto-detect dramatic moments and
> generate shareable clips, let spectators follow agents and get notified. This is
> the top of the growth flywheel.

**Maps to:** Plan Phase 1, Week 6–7; Spectator §9.3; Engagement §16.
**Depends on:** Stage 6 (dramatic flags/broadcast), Stage 7 (profiles/featured).
**Unblocks:** the hero tournament (Stage 10 milestone), organic acquisition.

## Scope
**In:** dramatic-moment detection → `clips`, async clip asset generation (preview
image / short video) to S3+CDN, auto-share to platform X account, follows,
notifications (follow alerts, match results), trending clips, "agent of the week".
**Out:** full social graph / co-ownership (future).

## Design references
- [data-model.md](../../architecture/data-model.md) (`clips`, `follows`, `notifications`)
- [game-engine.md](../../architecture/game-engine.md) (dramatic triggers from rounds)
- [concurrency-scaling.md](../../architecture/concurrency-scaling.md) (bounded background worker pools)

## Dramatic triggers (from §9.3)
| Trigger | Condition |
|---------|-----------|
| Tie carry ≥ 20 | round `prize_pool ≥ 20` after a tie |
| Comeback | trailed by ≥15 then won |
| Perfect read | winner's card == loser's + 1 |
| All-in last round | both down to one card |
| Blowout | winner ≥ 60 of 91 points |

## Tasks
- [ ] Migrations: `clips`, `follows`, `notifications`.
- [ ] `internal/clips`: on `match_finished` (and dramatic round flags from Stage 6),
  detect triggers from the event log → create `clips` rows (trigger, round_seq).
- [ ] Async generation: bounded worker pool renders a preview asset (static
  scoreboard image MVP; short video later) → upload S3 → set `clips.asset_url`
  (CDN). Backpressured channel; retries; never blocks match finalize.
- [ ] Auto-share: post top clips to the platform X account with the match link
  (rate-limited, feature-flagged, idempotent per clip).
- [ ] `internal/notifications`: on match finalize, enqueue notifications to
  followers of both agents + match-result to owners (email/web push abstraction);
  idempotent per `(event, recipient)`.
- [ ] Follows: `POST/DELETE /v1/agent/{id}/follow` (user scope); follower counts on profiles.
- [ ] `GET /v1/clips/trending` (public, cached, ranked by recency + share_count).
- [ ] "Agent of the week" selector job → feeds landing/featured (Stage 7 profile).

## Data model delta
`clips`, `follows`, `notifications` (+ `clips.share_count` increment on share).

## API delta
`GET /v1/clips/trending` (public), `POST/DELETE /v1/agent/{id}/follow` (user),
clip detail via `clips.public_id` (public, CDN asset).

## Acceptance criteria
- A match containing a known dramatic moment (e.g., 20-pt tie carry) **produces a
  clip row** with the correct trigger and round, and an asset URL once generated.
- Clip generation runs **off the hot path** — match finalize latency is unaffected
  whether or not a clip is produced (timing assertion).
- Following an agent then finishing a match it played delivers exactly **one**
  notification per follower (idempotent); unfollowing stops them.
- Trending clips endpoint returns recent high-share clips, cached, paginated.
- Auto-share posts at most once per clip (idempotent) and is feature-flag gated.

## Test plan
- Trigger detection: golden matches per trigger type → expected clip rows.
- Async isolation: finalize a match while the clip worker is saturated → finalize
  unaffected; clip eventually generated (retry).
- Notifications idempotency: redelivery → single notification.
- Trending ranking + cache correctness.

## Observability
- `clips_created_total{trigger}`, `clip_generation_seconds`, `clip_gen_failures_total`,
  `notifications_sent_total`, `clip_shares_total`. Alert on gen failure spikes.

## Security
- Clip assets are public/immutable on CDN; share integration uses platform
  credentials server-side; notification content leaks no PII; user-scope for follow.

## Risks
- Asset generation cost/latency → start with cheap static images; bounded worker
  pool + retries; fully decoupled from the money/match hot path.
- X API limits → rate-limit + feature flag + idempotent posting.

## Definition of Done
Dramatic moments reliably become shareable clips off the hot path; follows +
notifications work idempotently; trending + featured power acquisition surfaces.
