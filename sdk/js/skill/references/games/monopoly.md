# Monopoly — 2–8 players, board, near-perfect information

Standard rules: 4 players by default, $1500 start, $200 for passing GO. A phase
machine — roll, resolve where you land, then manage (build / mortgage / trade) and end
your turn.

Almost everything is exposed in `state`; only future randomness (unshuffled decks) is
hidden.

## Turn view

| Field | Type | Meaning |
| --- | --- | --- |
| `match_id` | str | Key your per-match state on this. |
| `seat` | int | Your seat. |
| `phase` | str | The decision owed — see below. |
| `legal_actions` | str[] | Exactly what you may do now. **Read this every turn.** |
| `state` | dict | The board: `players` (cash, position, jail, bankrupt), `holdings` (owner / houses / mortgaged per square), dice, current turn, pending auction or trade. |

`state` is a raw dict — inspect it rather than expecting fixed accessors. Because the
phase tells you the situation and `legal_actions` tells you exactly what is allowed,
**drive off those two** rather than trying to track fixed field names.

## Move

```json
{ "action": "<str>", "property": <int?>, "amount": <int?>, "trade": <object?>, "rationale": "<why>" }
```

`action` must be in `legal_actions`. `property` is a board-square index for `build` /
`mortgage` / `unmortgage` / `sell_house`. `amount` is your raise for `bid`. `trade` is
only for `propose_trade`:
`{proposer, target, give_props[], give_cash, want_props[], want_cash}`.

## Phases

| Phase | Decision |
| --- | --- |
| `roll` | Your turn — roll (or act from jail). |
| `jail` | Choose how to get out. |
| `acquire` | You landed on an unowned property — buy or decline. |
| `auction` | Someone declined a property — bid or pass. |
| `resolve_debt` | You owe more than your cash — raise funds or go bankrupt. |
| `manage` | Post-move: build / mortgage / trade, then `end_turn` (re-roll on doubles). |

## Budget

60s per decision by default — larger than the other games because the decisions are
larger. Miss it and the engine submits a safe legal action for you.

## What actually wins

- **Sets, not squares.** A monopoly with houses is worth far more than scattered
  property; price trades by whether they complete a set for you or for them.
- **Keep cash for `resolve_debt`.** Bankruptcy is the only true loss condition, and
  over-building into a rent spike is the usual cause.
- **Auctions are where value leaks.** Declining a property you want, then bidding
  poorly, hands it over cheaply.
- **Mortgage deliberately**, not in a panic — unmortgaging costs interest.
