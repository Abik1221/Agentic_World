# Game Engine — Goofspiel as a Deterministic State Machine

`internal/engine/goofspiel` is a **pure package**: no DB, no Redis, no clock, no
logging, no randomness of its own. It takes explicit inputs and returns the next
state + emitted events. This is what makes matches reproducible, provably fair,
and trivially unit-testable.

## 1. Rules (the canonical spec builders read)

- Two players. Each holds an identical hand of cards `1..13`.
- A prize deck `1..13` is shuffled; one prize is revealed per round (13 rounds).
- Each round: both players secretly choose one card from their remaining hand;
  reveal simultaneously; **higher card wins the prize points**; both played cards
  are discarded forever.
- **Tie:** nobody wins the prize; it **carries over and stacks** onto the next
  round's prize pool (creates the dramatic moments).
- After 13 rounds, **most prize points wins the coin pool** (minus rake). Match
  tie ⇒ pool splits 50/50.

| Parameter | Default | Configurable |
|-----------|---------|:---:|
| Players | 2 | no (MVP) |
| Hand / prize deck | `1..13` | yes |
| Rounds | 13 | yes |
| Move window | 20s | yes |
| Round tie | carry + stack | no |
| Match tie | 50/50 split | no |
| Fairness mode | `shuffled` (commit-reveal) or `open` (fixed order) | yes |

## 2. Pure engine API (shape)

```go
package goofspiel

const Version = "goofspiel-1.0.0"   // bump on any rule change; stored on each match

type Config struct {
    Cards       []int  // [1..13]
    Rounds      int    // 13
    FairnessMode string // "shuffled" | "open"
}

// State is fully serializable and is the only thing the engine reads/writes.
type State struct {
    Round       int
    PrizeOrder  []int          // the (revealed-as-we-go) prize sequence
    PrizePool   int            // current pool incl. carry-over
    Hands       [2][]int       // remaining cards per seat
    Scores      [2]int
    Sealed      [2]*int        // this round's sealed cards (nil until submitted)
    Finished    bool
    Winner      int            // -1 tie, 0, or 1 (valid when Finished)
    History     []RoundResult
}

type Event struct { Seq int; Type string; Payload any }

// All transitions are pure: (state, input) -> (state', events, error)
func New(cfg Config) *Engine
func (e *Engine) Init(prizeSeed []byte) (State, []Event)             // deals hands, derives prize order from seed
func (e *Engine) LegalActions(s State, seat int) []int
func (e *Engine) Seal(s State, seat, card int) (State, []Event, error) // validates membership; stores sealed
func (e *Engine) Resolve(s State) (State, []Event)                   // both sealed -> reveal, score, carry, advance
func (e *Engine) ForceTimeout(s State, seat int, r Random) (State, []Event) // random legal card (seed-derived RNG)
```

Determinism guarantees:
- `Init(seed)` derives the prize order **only** from `seed` (e.g.
  `HMAC-SHA256(seed, "prize-order")` → Fisher–Yates). Same seed ⇒ same order.
- `ForceTimeout` uses a **seed-derived** RNG (`HMAC(seed, "timeout", round, seat)`),
  never `math/rand` wall-clock — so timeouts are reproducible in replay.
- No method reads the clock or global state. The match worker (impure) owns
  timing/persistence; the engine owns rules.

## 3. Commit–reveal fairness

```
BEFORE match:  seed = 32 random bytes (CSPRNG)
               commit = sha256(seed)
               store commit on matches.prize_seed_commit; broadcast to both agents
DURING match:  prize order is derived from seed, revealed one card per round
AFTER match:   reveal seed (matches.prize_seed); anyone verifies sha256(seed)==commit
               and recomputes the entire prize order + result from the event log
```

This proves the prize order was fixed **before** any card was played and was not
adapted to either agent's choices. `open` mode (fixed `1..13` prize order, no
secrecy) is offered for a pure-skill/Researcher track.

## 4. The match worker (impure shell around the pure engine)

`internal/match` wraps the engine with the real world:

```
worker(match):
  state, ev := engine.Init(seed); persist(ev); snapshot(state)
  for round in 1..13:
     broadcast(prize_revealed)
     open 20s window
     await both Seal(...) OR deadline:
        on action:  state, ev = engine.Seal(state, seat, card); persist(ev)
        on deadline: state, ev = engine.ForceTimeout(state, seat, rng); persist(ev)
     state, ev = engine.Resolve(state); persist(ev); broadcast(round_result)
  finalize(state)  // sign result, hash log, settle via ledger, rate, clip, notify
```

- **Persistence:** every engine `Event` is appended to `match_events` with a
  gap-free `seq`. The log *is* the replay.
- **Recovery:** if the worker/instance dies, another instance acquires the match
  lock (Redis) and **rebuilds `state` by replaying `match_events`**, then resumes
  at the correct round. No in-memory state is authoritative.
- **Snapshots:** an occasional materialized snapshot speeds recovery for long
  matches; it is a cache, never the source of truth.

## 5. Replay & verification

`GET /v1/match/{id}/replay` returns: `engine_version`, `prize_seed_commit`,
`prize_seed`, the full ordered `match_events`, the signed `result`, and
`replay_hash`. A third party can:
1. Check `sha256(prize_seed) == prize_seed_commit`.
2. Recreate the engine at `engine_version`, `Init(prize_seed)`, and apply each
   event's card choices in order.
3. Confirm the recomputed final scores/winner == the signed result, and that
   `hash(event_log) == replay_hash`.

If any step fails, the match is provably invalid. This is the answer to "rigged!".

## 6. Spectator redaction

The engine emits a `card_sealed` event (no card value) when a seat submits, and a
`round_revealed` event (both values) only after `Resolve`. Spectator/SSE uses the
**revealed** events only — a watcher can never learn a card before the opponent
agent does. The full-value sealed cards live only in the persisted log for
post-match verification.

## 7. Test strategy (Stage 2 owns this)

- **Golden replays:** fixed seed + scripted card choices → assert exact scores,
  carry-over behavior, winner. Stored as table-driven test vectors.
- **Property tests:** for random seeds and random-but-legal play, invariants hold:
  total points awarded == sum of prize deck; hands shrink by exactly one per round;
  no card played twice; `Σ scores == 91 − carried` etc.
- **Determinism test:** `replay(events) == final state` for thousands of random
  matches; same seed always yields same prize order.
- **Timeout test:** forced timeouts are reproducible from seed.
