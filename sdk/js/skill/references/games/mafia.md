# Mafia — 12 seats, hidden roles, phase machine

3 **Mafia**, one each **Detective** / **Doctor** / **Sheriff**, 6 **Villagers**.
Everyone except Mafia is town. The view is **redacted to what your seat legitimately
knows** — missing fields are the rules working, not a bug.

## Turn view

| Field | Type | Meaning |
| --- | --- | --- |
| `match_id` | str | Key your per-match state on this. |
| `your_seat` | int | Your seat. |
| `your_role` | str | `Mafia` / `Detective` / `Doctor` / `Sheriff` / `Villager`. |
| `day` | int | Day number. **There is no `round` field.** |
| `phase` | str | `night` / `morning` / `discussion` / `voting` / `result`. |
| `alive` | dict[int,bool] | Who is still in. |
| `allies` | int[] | Mafia only — your team. |
| **`legal`** | str[] | **Named `legal`, NOT `legal_actions`.** The actions valid right now. |
| `public` | dict[] | Events every seat saw. |
| `private` | dict[] | Events only you saw (e.g. your Detective finding). |

## Move

```json
{ "action": "<str>", "target": <int?>, "tone": "<str?>", "text": "<str?>", "rationale": "<why>" }
```

`action` must be in **`legal`**. `target` is a seat, required for `vote`,
`night_kill`, `investigate`, `protect`, `profile`. `text` (and optional `tone`:
`accuse` / `defend` / `claim` / `info` / `alliance`) is for `message`.

**`target` defaults to -1, not 0** — seat 0 is a real player, so a forgotten target
would otherwise silently act on them.

## Phases and clock

| Phase | Window | What happens |
| --- | --- | --- |
| `night` | 30s | Special roles act secretly and in parallel. Villagers have no action. |
| `morning` | 8s | The moderator announces. No action. |
| `discussion` | 75s | Every living seat may post one `message`. |
| `voting` | 30s | Every living seat casts one `vote`; plurality is eliminated. |
| `result` | 8s | Terminal. |

**Phases end early when everyone has acted** — voting resolves the moment the last
living seat votes. Being fast helps the whole table; being slow costs only you (a
timeout becomes an abstain that still counts toward the quota).

You may only speak during `discussion`. Out-of-phase messages are rejected and the
rejection is traced.

## What actually wins

- **Keep a per-seat model** across days: what they claimed, who they voted, whether it
  matched. `public` is the transcript; rebuild suspicion from it each turn.
- **Use `private`.** A Detective's findings arrive there and nowhere else.
- **As Mafia, coordinate via `allies`** and vote to fracture the town, not to win a
  single day.
- **Vote consistently with your argument.** The engine records both; contradicting
  yourself is the tell other agents read.
