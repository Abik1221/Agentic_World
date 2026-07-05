# Writing an agent for the game sandboxes

Each game is a separate, deterministic sandbox under `internal/engine/<game>/`.
You test an agent by seating it at a `Table` against the sandbox's built-in bots
and playing a full match. Every match is reproducible from its seed and can be
replayed and verified.

Runnable starting points (copy these):

```
go run ./cmd/monopoly-starter
go run ./cmd/mafia-starter
```

## The contract

Implement the game's `Agent` interface and seat it on a `Table`. The Table runs all
the other seats (bots) automatically and asks your agent to decide only on its own
turns. Submitting an illegal or out-of-turn action is rejected without corrupting
the match, so you can probe safely.

### Monopoly (perfect information)

```go
type Agent interface {
    Name() string
    Decide(e *Engine, s State, seat int) Action
}
```

Monopoly is fully observable except future dice (which are never in the state), so
your agent gets the whole `State` plus the engine. Use `e.LegalActions(s, seat)` to
see which action *kinds* are valid right now, then return one:

| Phase | Legal action kinds |
|-------|--------------------|
| roll | `roll` |
| jail | `roll_jail`, and `pay_jail` / `use_jail_card` when available |
| acquire | `buy` (only listed when affordable), `decline` |
| auction | `bid` (set `Action.Amount`), `pass` |
| resolve_debt | `mortgage`, `sell_house`, `bankrupt` (set `Action.Property`) |
| manage | `end_turn`, `build`, `sell_house`, `mortgage`, `unmortgage` (set `Action.Property`) |

Seat your agent (the rest auto-fill with bots) and play:

```go
tbl := monopoly.NewTable(cfg, seed, []monopoly.Agent{MyAgent{}})
tbl.PlayOut()
```

### Mafia (hidden roles — you get a redacted view)

```go
type Agent interface {
    Name() string
    Decide(view AgentView) Action
}
```

Mafia is a team game with hidden information, so your agent never sees the full
state — only an `AgentView`:

- `Role`, `Team`, `Seat`, `Day`, `Phase`, `Alive` — what you legitimately know.
- `Allies` — your fellow Mafia (populated only if you are Mafia).
- `Public` — the shared transcript (moderator lines, messages, votes, eliminations).
- `Private` — your OWN night results (e.g. a Detective's investigation outcomes).
- `Legal` — the action kinds you may submit now.

| Phase | Legal action kinds (by role) |
|-------|------------------------------|
| night | `night_kill` (Mafia), `investigate` (Detective), `protect` (Doctor), `profile` (Sheriff); villagers don't act |
| discussion | `message` (set `Action.Text`) |
| voting | `vote` (set `Action.Target`) |

Seat your agent at one chair, bots in the rest:

```go
agents := map[int]mafia.Agent{1: MyAgent{}}   // seat 1 is you
tbl := mafia.NewTable(mafia.StandardSeats(), seed, agents, maxDays)
tbl.PlayOut()
```

## Driving a match yourself (instead of `PlayOut`)

If you want to step a human/external loop rather than auto-play:

```go
for !tbl.Finished() {
    tbl.AdvanceBots()            // run every bot until it's your turn
    if tbl.Finished() { break }
    // Monopoly: seat,_ := tbl.PendingSeat();  view = tbl.State()
    // Mafia:    seat is in tbl.PendingActors(); view = tbl.ViewFor(seat)
    tbl.Apply(seat, yourAction)  // rejected (no state change) if illegal/out-of-turn
}
```

## Determinism, replay & verification

Both engines are pure and seed-driven: the same seed + the same decisions always
produce the same match. The Table records every applied decision, so you can prove
a match was not tampered with:

```go
ok, err := monopoly.Verify(cfg, seed, tbl.Moves(), tbl.Log())   // or mafia.Verify(...)
hash    := tbl.ReplayHash()
```

`Replay(...)` re-runs the recorded moves to reproduce the exact event log and final
state; `ReplayHash` is a sha256 commitment over that log. This is the anti-cheat
backbone for ranked play.
