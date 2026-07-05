# Platform Config & Event Bus — cross-service contract

The **Super Admin** is the configuration source of truth; the **Main Backend**
(game engine) is the execution engine. They are decoupled and communicate
**asynchronously over Redis** — the only shared infrastructure. There is no
synchronous RPC between them: the engine must keep executing matches on its
last-known-good config even while the Super Admin is restarting or unreachable.

Two independent planes:

| Plane | Direction | Redis primitive |
|---|---|---|
| **Config** | Super Admin → Engine | Snapshot key + version + Pub/Sub invalidation |
| **Events** | Engine → Super Admin (dashboard/analytics) | Stream + consumer group |

Both services already vendor `github.com/redis/go-redis/v9` and share one Redis
(`REDIS_URL`). All keys are namespaced under `platform:`.

---

## 1. Config plane (Super Admin → Engine)

### Keys / channels

| Name | Type | Meaning |
|---|---|---|
| `platform:config:snapshot` | String (JSON) | The full config bundle the engine consumes. Authoritative. |
| `platform:config:version` | String (int64) | Monotonic version, bumped on every publish. Mirrors `snapshot.version`. |
| `platform:config:changed` | Pub/Sub channel | Signal only; message body is the new version as a decimal string. |

### Protocol

**Publisher (Super Admin)** — on startup and after every config mutation:
1. Build the snapshot from Postgres (live season + its `season_settings`,
   `season_rewards`, `economy_config`, `feature_flags`, games, SDK requirements).
2. Bump `platform:config:version` (`INCR`), set `snapshot.version` to it.
3. `SET platform:config:snapshot <json>` (no TTL — it is the durable cache).
4. `PUBLISH platform:config:changed <version>`.

The publish is idempotent by version: re-publishing the same logical state just
produces a newer version number, which is harmless.

**Consumer (Engine)**:
1. On boot: `GET platform:config:snapshot`. If present and parseable, adopt it.
   If absent/unreachable/garbage, adopt **built-in env defaults** and keep running.
2. Subscribe to `platform:config:changed`. On any message, re-`GET` the snapshot
   and atomically swap the in-memory pointer. The message is a pure wake-up — the
   engine always re-reads the authoritative key (a dropped/duplicated message only
   affects refresh latency, never correctness — same idiom as `store/notifier.go`).
3. A periodic re-pull ticker (default 60s) is the backstop for a missed pub/sub
   message, and the recovery path once Redis comes back.

Fallback rule: the engine **never** blocks or fails a match because config is
stale or unavailable. Worst case it runs on the last snapshot it saw, or on env
defaults if it never saw one.

### Snapshot schema (`platform:config:snapshot`)

```jsonc
{
  "version": 42,                       // matches platform:config:version
  "generated_at": "2026-07-04T12:00:00Z",
  "active_season": {                   // null when no season is live → ranked rejected
    "code": "S3", "name": "Season 3", "number": 3,
    "status": "live",                  // draft|scheduled|live|paused|completed|archived
    "ranked_enabled": true,
    "registration_enabled": true,
    "start_date": "2026-07-01T00:00:00Z",
    "end_date":   "2026-08-01T00:00:00Z"
  },
  "points": {                          // season_settings category=points
    "win": 10, "loss": 2, "draw": 5,
    "participation_bonus": 1, "win_streak_bonus": 3,
    "timeout_penalty": -5, "disconnect_penalty": -8,
    "illegal_move_penalty": -10, "abandon_penalty": -15,
    "max_points_per_day": 500
  },
  "ranking": {                         // season_settings category=ranking (formula weights)
    "win_weight": 1.0, "opponent_strength_weight": 0.5,
    "consistency_weight": 0.3, "activity_weight": 0.2,
    "win_rate_weight": 0.4, "penalty_weight": 1.0
  },
  "match_rules": {                     // season_settings category=match_rules
    "min_coins": 50, "max_coins": 5000,
    "min_players": 2, "max_players": 15,
    "turn_timeout_sec": 30, "match_timeout_sec": 1200,
    "reconnect_timeout_sec": 60, "ai_response_timeout_sec": 8,
    "max_daily_ranked_matches": 100,
    "certification_required": true
  },
  "automation": {                      // season_settings category=automation
    "auto_reward_distribution": true, "auto_hall_of_fame_update": true,
    "auto_leaderboard_refresh": true, "auto_archive": true,
    "max_seasons_stored": 50
  },
  "rewards": [                         // season_rewards, ordered
    { "tier": "champion", "reward_type": "coins", "amount": 1000, "description": "…", "ordering": 1 }
  ],
  "economy": {                         // economy_config (key/value → typed)
    "platform_commission_pct": 8,
    "coin_price_cents_per_100": 99,
    "min_purchase_cents": 500, "max_purchase_cents": 200000,
    "min_withdrawal_cents": 2000, "max_withdrawal_cents": 1000000,
    "referral_reward_coins": 500
  },
  "feature_flags": {                   // feature_flags table
    "tournament_v2": { "enabled": false, "rollout": "5%" }
  },
  "sdk_requirements": {                // Documentation-Service driven version gates
    "supported_manifest_versions": ["1.1"],
    "min_sdk_versions": { "go": "0.3.0", "python": "0.3.0" },
    "docs_base_url": "https://my-mega-hub.com/docs"
  },
  "games": [                           // games table
    { "code": "mafia", "engine": "v1", "live": true },
    { "code": "goofspiel", "engine": "v1", "live": true },
    { "code": "monopoly", "engine": "v1", "live": true }
  ]
}
```

Every field is optional on the wire: a missing/zero field falls back to the
engine's env default for that value, so the two services can be deployed and
evolved independently.

---

## 2. Event plane (Engine → Super Admin)

### Keys

| Name | Type | Meaning |
|---|---|---|
| `platform:events` | Stream | Append-only domain-event log. |
| `platform:events:dead` | Stream | Dead-letter for events a consumer repeatedly fails to process. |
| consumer group `superadmin` | Group on `platform:events` | Super Admin's durable read cursor. |

### Protocol

The engine already runs a **transactional outbox** (`internal/events`): every
domain fact is written in the same DB tx as the state change, then the dispatcher
delivers it to in-process handlers at-least-once. We add **one more handler** that
mirrors each delivered event onto the `platform:events` stream:

```
XADD platform:events * id <evt_id> type <type> payload <json> ts <unixMs>
```

`id` is the outbox event's public id (`evt_…`) — the **idempotency key**. The
Super Admin consumer keys off it so at-least-once re-delivery is safe.

Event types (past-tense facts, extend as producers emit them):
`agent.certified`, `match.finished`, `season.rolled`, `badge.awarded`, plus the
spec's `match.started`, `match.cancelled`, `replay.generated`,
`leaderboard.updated`, `season.started`, `season.ended`, `reward.granted`,
`hall_of_fame.updated`, `agent.suspended`.

**Consumer (Super Admin)**: `XREADGROUP` on group `superadmin`, project events into
the dashboard (live feed via the existing `coder/websocket` hub) and analytics
tables, then `XACK`. Poison messages (repeated failures) are copied to
`platform:events:dead` and acked so they can never wedge the group.

---

## 3. Why Redis and not gRPC

- Both services already depend on go-redis; neither has a protobuf toolchain.
- The config requirement is explicitly *"cache locally with fallback if the admin
  service is unavailable"* — an async, decoupled pattern. Sync RPC would couple
  engine availability to the Super Admin being up.
- Redis Streams give durable, ordered, replayable at-least-once delivery with
  consumer groups and a natural dead-letter — exactly the event backbone the spec
  asks for — with no new infrastructure.
