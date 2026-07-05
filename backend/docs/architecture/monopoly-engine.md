# Game Engine — Monopoly as a Deterministic State Machine

`internal/engine/monopoly` is the **second game sandbox** in the arena, built to
the same contract as `internal/engine/goofspiel`: a **pure package** with no DB,
no Redis, no clock, no logging, and no randomness of its own. Every transition is
a function `(state, input) -> (state', events, error)`. This is what makes a
match reproducible, provably fair, replayable, and trivially unit-testable.

The engine is dedicated to Monopoly only. It shares no rules code with the card
game; the two engines coexist under `internal/engine/<game>/` and are wired into
the (impure) match worker independently, exactly as `goofspiel` and `mafia` are.

## 1. Rules (the canonical spec builders read)

- 2–8 players (free-for-all), each starting with $1500 on the classic 40-square
  US board.
- On a turn a player rolls two dice and advances. **Doubles** grant another roll;
  **three doubles in a row** sends the player straight to jail.
- Landing rules: buy an unowned property at list price or send it to **auction**;
  pay **rent** on an owned property; pay **tax**; draw **Chance / Community
  Chest**; **Go To Jail**; collect **$200** for passing GO.
- Rent: streets pay a base/1–4 house/hotel tier (double base on an unimproved
  full color set); railroads pay by count owned (25/50/100/200); utilities pay
  4× or 10× the dice.
- Build houses/hotels under the **even-build** rule on a full, unmortgaged color
  group, constrained by the bank's supply of **32 houses and 12 hotels**.
- **Mortgage** for half the list price; **unmortgage** for that value + 10%.
- **Jail:** escape by rolling doubles, paying $50, or a Get-Out-of-Jail card;
  after a third failed roll the $50 is forced.
- **Bankruptcy** eliminates a player; their estate transfers to the creditor (or
  the bank). The **last solvent player wins**. A turn cap (`MaxTurns`) guarantees
  termination; if reached, the highest **net worth** wins.

| Parameter | Default | Configurable |
|-----------|---------|:---:|
| Players | 4 | yes (2–8) |
| Starting cash | $1500 | yes |
| GO salary | $200 | yes |
| Bank supply | 32 houses / 12 hotels | no |
| Auctions on decline | on | yes |
| Free Parking jackpot | off (house rule) | yes |
| Turn cap | 1000 rolls | yes |

## 2. Pure engine API (shape)

```go
package monopoly

const Version = "monopoly-1.0.0"   // bump on any rule change; stored on each match

type Config struct {
    Players, StartingCash, MaxTurns, GoSalary int
    DisableAuctions, FreeParkingPool          bool   // both default false (auctions ON)
}

// State is fully serializable and is the only thing the engine reads/writes.
type State struct {
    Players  []Player
    Holdings []Holding   // length 40, indexed by board square
    Current  int
    Phase    string      // roll | jail | acquire | auction | resolve_debt | manage | game_over
    HousesRemaining, HotelsRemaining int
    LastRoll [2]int
    RollSeq  int          // drives the deterministic dice stream
    ChanceOrder, CCOrder []int   // seed-shuffled decks (+ wrap-around indices)
    Auction *AuctionState
    Debt    *Debt
    TurnCount int
    Finished  bool
    Winner    int          // seat or Tie
    NextSeq   int
}

type Action struct { Kind string; Property int; Amount int }

// All transitions are pure: (state, input) -> (state', events, error)
func New(cfg Config) *Engine
func (e *Engine) Init(seed []byte) (State, []Event)              // deals cash, shuffles decks from the seed
func (e *Engine) LegalActions(s State, seat int) []string        // valid action kinds for a seat right now
func (e *Engine) Step(s State, seat int, a Action, seed []byte) (State, []Event, error)
func (e *Engine) ForceTimeout(s State, seed []byte) (State, []Event, error) // deterministic default for a missed decision
```

`Step` is transactional: on any error the **original** state is returned
unchanged. `LegalActions` (and `ForceTimeout`) consult `pendingActor`, which is
the auction's current bidder during `auction`, the debtor during `resolve_debt`,
and otherwise the current player.

## 3. Determinism & commit–reveal fairness

All chance is a function of the match seed:

- **Dice:** roll number `n` is `HMAC-SHA256(seed, "dice:n")` → two faces. The
  state's `RollSeq` advances on every roll, so replaying the same action log from
  the same seed reproduces every roll exactly, regardless of which player rolled.
- **Decks:** Chance and Community Chest are seed-shuffled (`HMAC(seed, "deck:…")`
  → Fisher–Yates) at `Init` and drawn top-to-bottom with wrap-around.
- **Forced timeouts** use a seed-derived `Rand` (`HMAC(seed, "timeout:turn:seat")`),
  never `math/rand` or the clock.

```
BEFORE match:  seed = 32 random bytes (CSPRNG); commit = sha256(seed)
               store commit on the match; broadcast to players
DURING match:  dice + decks are derived from seed; the event log records every move
AFTER match:   reveal seed; anyone verifies sha256(seed) == commit and re-derives
               every roll/card + recomputes the result from the event log
```

This proves the dice and decks were fixed **before** play and were not adapted to
any player's choices.

## 4. The match worker (impure shell around the pure engine)

`internal/match` wraps the engine with the real world — identical pattern to the
card game:

```
worker(match):
  state, ev := engine.Init(seed); persist(ev); snapshot(state)
  loop until state.Finished:
     actor := pendingActor(state); open the decision window
     await actor's Action OR deadline:
        on action:  state, ev, err = engine.Step(state, actor, action, seed)
        on deadline: state, ev, _  = engine.ForceTimeout(state, seed)
     persist(ev); broadcast(redacted ev)
  finalize(state)   // sign result, hash log, settle via ledger, rate, clip, notify
```

- **Persistence:** every `Event` is appended with a gap-free `seq`. The log *is*
  the replay.
- **Recovery:** rebuild `state` by replaying `match_events` from the seed; no
  in-memory state is authoritative.

## 5. Replay & verification

`GET /v1/match/{id}/replay` returns `engine_version`, the seed commit + revealed
seed, the ordered `match_events`, the signed result, and `replay_hash`. A third
party re-creates the engine at `engine_version`, `Init(seed)`, applies each
action in order, and confirms the recomputed winner/net-worths and the log hash.
Any mismatch proves the match invalid.

## 6. Money is not conserved (and that's expected)

Unlike a zero-sum card pot, Monopoly's bank injects money (GO salary, card
collects) and absorbs it (taxes, fines, house purchases). The engine therefore
does **not** assert global money conservation. The invariants it *does* guarantee
(enforced by tests) are: no negative cash; bank building supply is conserved
(`houses_on_board + houses_remaining == 32`, `hotels_on_board + hotels_remaining
== 12`); bankrupt players own nothing and hold no cash; positions stay on-board;
the event log is gap-free; and every game terminates.

### Documented simplifications (v1.0.0)

These keep the engine bounded and fully deterministic; each is a deliberate,
testable choice rather than a bug:

- **Bank auctions on bankruptcy:** properties surrendered to the *bank* return
  unimproved rather than being re-auctioned.
- **Multi-party cards:** "collect from each" takes `min(cash, amount)` from each
  player (no cross-player debt); "pay each player" routes a shortfall to a single
  bank debt that the player must cover or go bankrupt against.
- **Mortgaged transfers** on bankruptcy carry to the new owner without immediate
  interest.

### Player-to-player trading (added in 1.1.0)

During `manage`, a player may `propose_trade` (an `Action.Trade` of properties +
cash either way) to another seat, opening a `trade_response` phase where the target
`accept_trade`s or `reject_trade`s. Trades are validated (real ownership, no
buildings anywhere in a traded color group, sufficient cash); accepting swaps the
properties and nets the cash. Built-in bots accept trades that gain them list-price
value but never initiate, so unattended games stay stable while a user's agent can
trade freely.

### Replay & verification (added in 1.1.0)

The `Table` records every applied `(seat, action)` as a `Move`. `Replay(cfg, seed,
moves)` re-runs them to reproduce the exact event log and final state;
`ReplayHash(events)` is a sha256 commitment over the log, and `Verify(...)` proves
a claimed log matches a replay — the anti-cheat backbone for ranked play.

## 7. Dedicated runtime: the Table + bot agents

The pure engine is rules only. `runner.go` adds a **`Table`** — the dedicated,
embeddable environment that actually plays a match: it owns the live `State`, the
event log, the seed, and the **seating**. This is what lets a single user join and
experience a full multiplayer game.

```go
agents := []monopoly.Agent{
    nil,                                              // seat 0 = the human
    monopoly.NewBot("Borg", monopoly.StyleTycoon, seed, 1),
    monopoly.NewBot("Cleo", monopoly.StyleBanker, seed, 2),
    monopoly.NewBot("Dax",  monopoly.StyleWildcard, seed, 3),
}
tbl := monopoly.NewTable(cfg, seed, agents)   // nil agents auto-fill with bots

for !tbl.Finished() {
    tbl.AdvanceBots()                 // play every bot until it's the human's turn
    if tbl.Finished() { break }
    seat, _ := tbl.PendingSeat()      // == the human seat
    // show tbl.LegalActions(seat) to the user, collect their choice, then:
    tbl.Apply(seat, userAction)       // transactional: rejects illegal/out-of-turn moves
}
```

**Bot agents** (`agent.go`) fill every seat the user does not occupy and play like
real opponents — buying, completing color sets, bidding in auctions, building,
managing jail, and raising funds before going bankrupt. They are **pure and
deterministic**: each bot's choices come from a seed-derived stream keyed by its
seat, so a table replays identically. Four personalities ship in
(`StyleTycoon`, `StyleBanker`, `StyleCautious`, `StyleWildcard`); bots only ever
return *legal* actions, and the Table additionally falls back to `ForceTimeout` if
an agent ever misbehaves — a bad bot can never wedge a match.

**Robustness guarantees of the Table:** `Apply` is transactional (illegal or
out-of-turn human moves leave the state untouched and return an error to re-prompt);
`AdvanceBots`/`PlayOut` are bounded by `MaxSteps` so no logic error can spin forever;
`TimeoutPending` resolves an abandoned human seat deterministically; and the whole
table is reproducible from its seed.

A runnable demonstration lives at `cmd/monopoly-demo`:

```
go run ./cmd/monopoly-demo                        # watch four bots play a full game
go run ./cmd/monopoly-demo -players 6 -seed foo   # six bots, fixed seed, repeatable
go run ./cmd/monopoly-demo -interactive -human 0  # take seat 0; the sandbox plays the rest
```

It plays a complete match and prints a transcript plus final standings (cash, net
worth, holdings, and the winner).

## 8. Test strategy (mirrors the card game's)

- **Unit / golden tests** (`engine_test.go`, `board_test.go`): board integrity,
  group counts, known prices/rents, buy, full-set rent doubling, railroad/utility
  rent, even-build + supply, mortgage/unmortgage with interest, go-to-jail, card
  effects, the auction flow, out-of-turn rejection, debt auto-settle, and
  bankruptcy ending a two-player game.
- **Property tests** (`determinism_test.go`): 200 random-but-legal full games
  asserting every structural invariant after each step, and that each game ends
  with a valid winner and a `match_finished` event.
- **Determinism tests:** same seed + same policy reproduces the match byte-for-byte
  (final state and full event stream); a fully `ForceTimeout`-driven game is
  identical across runs; decks are reproducible permutations.
- **Runtime tests** (`runner_test.go`): the Table plays full bot games across many
  seeds and player counts to a valid winner; self-play is deterministic
  byte-for-byte; the human-seat flow completes; `Apply` rejects illegal/out-of-turn
  moves transactionally; nil seats auto-fill with bots.

Run them with:

```
go test ./internal/engine/monopoly/...   # full suite
go run  ./cmd/monopoly-demo               # play a full game and print the transcript
```
