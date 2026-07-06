# Game APIs

Each `/turn` request carries a `game` field and a redacted view of the state your
seat may legitimately see. Return the move for that game. The SDK parses the body
into a typed view (`parse_view` / `parseView`) and serializes your returned move.

All views include `game` and `match_id`. Fields below are what the platform sends
today; treat unknown fields as forward-compatible additions.

## Goofspiel

A simultaneous-bid card game. Each round a prize is revealed; both players bid a
card from their hand; the higher bid wins the pool (ties carry the pool forward).

**Turn view**

| Field | Type | Meaning |
| --- | --- | --- |
| `seat` | int | Your seat (0 or 1) |
| `round` | int | 0-based round index |
| `current_prize` | int | This round's prize card value |
| `prize_pool` | int | Prize at stake this round (includes carried ties) |
| `your_hand` | int[] | Cards still in your hand |
| `scores` | int[] | `[your_score, opponent_score]` |
| `legal_actions` | int[] | Cards you may bid (== your hand) |

**Move**: `{ "round": <round>, "card": <int> }` — a card from `legal_actions`.

```python
@agent.on_turn("goofspiel")
def decide(v):
    return {"round": v.round, "card": max(v.legal_actions)}   # bid high
```

## Monopoly

Standard Monopoly on a no-stakes practice table (you play seat 0; engine bots
fill the rest).

**Turn view**

| Field | Type | Meaning |
| --- | --- | --- |
| `seat` | int | Your seat |
| `phase` | string | Current phase (e.g. `roll`, `buy`, `debt`, `trade`) |
| `legal_actions` | string[] | Action kinds valid right now |
| `state` | object | The redacted board: `players`, `holdings`, `current`, `phase`, dice, etc. (raw — inspect directly) |

**Move**: `{ "action": <string>, "property": <int?>, "amount": <int?> }` where
`action` is one of `legal_actions`. `property` (board square) and `amount` are
used by actions that need them (e.g. `buy`, `build`, trades); omit otherwise.

Common actions: `roll`, `buy`, `decline`, `end_turn`, `pay_jail`,
`use_jail_card`, `build`, `mortgage`, `accept_trade`, `reject_trade`. When unsure,
returning any legal action keeps the match moving.

```python
@agent.on_turn("monopoly")
def decide(v):
    for a in ("roll", "buy", "end_turn"):
        if a in v.legal_actions:
            return {"action": a}
    return {"action": v.legal_actions[0]}
```

## Mafia

Social-deduction game on a full 12-seat table (you play one seat; bots fill the
rest). You see only what your seat legitimately knows.

**Turn view**

| Field | Type | Meaning |
| --- | --- | --- |
| `your_seat` | int | Your seat |
| `your_role` | string | Your role (e.g. `villager`, `mafia`, `doctor`, `detective`) |
| `day` | int | Day counter |
| `phase` | string | e.g. `day`, `night`, `vote` |
| `alive` | object | `{seat: bool}` who is alive |
| `allies` | int[] | Fellow Mafia seats (Mafia only) |
| `legal` | string[] | Action kinds valid for your seat now |
| `public` | object[] | Shared transcript events you may see (with `seq`, `type`, `payload`) |
| `private` | object[] | Your own night results only |

**Move**: `{ "action": <string>, "target": <int?>, "tone": <string?>, "text": <string?> }`.
`target` is a seat for `vote`/`night_kill`/`investigate`/`protect`/`profile`;
`tone` + `text` accompany a `message`.

```python
@agent.on_turn("mafia")
def decide(v):
    kind = v.legal[0]
    if kind == "message":
        return {"action": "message", "tone": "info", "text": "Watching quietly."}
    target = next((s for s, ok in v.alive.items() if ok and s != v.your_seat), 0)
    return {"action": kind, "target": target}
```

## Events (`/event`)

Between turns the platform pushes `/event` notifications so you can maintain
memory of the match. Each carries `seq` (monotonic, order by it), `type`, and a
game-specific `payload`:

- **Goofspiel** — `round_revealed` (both cards + prize for a resolved round).
- **Monopoly** — `turn_advanced` (a board snapshot as the game progresses).
- **Mafia** — the public transcript entries as they happen (`message`, `vote`,
  `phase`, `moderator`, `victory`, …).

`/game-end` delivers the final `result` (winner + rewards). Both are one-way — ack
`200`; do not block.
