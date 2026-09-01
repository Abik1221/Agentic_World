"""Mafia: 12 seats of hidden-role deduction, and the only game here with teammates.

# What this game measures that the other two do not

Goofspiel and Monopoly measure a model against an opponent. Mafia measures it against a
SOCIAL field: three Mafia who must converge without speaking plainly, a Town that must read
intent out of a transcript, and a Doctor whose whole role is choosing who goes unguarded.
That makes it the most interesting arena for a model-vs-model claim and the most expensive,
because twelve seats mean twelve model calls per phase.

# Two rules that decide more matches than strategy does

The mafia's kill is a PLURARITY across the three of them, and a three-way split kills nobody.
The view carries `ally_kills` so they can converge, and a model that ignores it wastes nights.
Second: the doctor may not shield the same seat twice running, and `cannot_protect` names the
barred seat. Discovering that rule by having a move refused costs a decision and a model call
to learn something the engine already said. Both are stated in the prompt for the same reason
the Goofspiel deduction is: we are measuring play, not rule rediscovery.

# Why the message text does not ride in the tool call

The bound move for Mafia is `kind:target` — the verb and the seat, and deliberately nothing
else. A message's TEXT is not part of the canonical form, so it cannot be bound and must not
be smuggled into the tool schema. The agent therefore takes the verb from the tool call (which
binds) and the message body from the model's ordinary text content (which does not). That
split is the honest one: the platform can prove which seat you voted for, and makes no claim
about what you said.
"""

from __future__ import annotations

from typing import Any

GAME = "mafia"

#: Wire convention for "this action names no seat". NEVER 0 — seat 0 is a real player, and a
#: forgotten target that defaulted to 0 would silently act against them.
NO_TARGET = -1

_SYSTEM = """\
You are an expert Mafia player competing in the Pyyol arena against other AI agents at a \
12-seat table. Play to win for YOUR team.

## The table

12 seats: 3 Mafia, 1 Detective, 1 Doctor, 1 Sheriff, 6 Villagers. Everyone except the Mafia \
is on the TOWN team. Role names are capitalised exactly as written here.

Town wins when every Mafia is eliminated. Mafia wins the moment living Mafia EQUAL OR \
OUTNUMBER living Town — at that point they can no longer be voted out, so Town must never \
let the count get there.

## Phases

- `night` — special roles submit one secret action. Villagers have none.
- `morning` — the moderator announces the night's outcome. No action from you.
- `discussion` — every living seat may post one message.
- `voting` — every living seat casts one vote. The plurality target is eliminated.
- `result` — the match is over.

## Actions

`night_kill` (Mafia, night), `investigate` (Detective, night), `protect` (Doctor, night), \
`profile` (Sheriff, night), `message` (discussion), `vote` (voting). Only ever submit an \
action listed as legal for you right now.

Targets are seat numbers. When an action names no seat, use -1. NEVER use 0 to mean \
"nobody" — seat 0 is a real player and 0 is a vote against them.

## Rules that decide matches

1. **The Mafia kill is a plurality, and a three-way split kills nobody.** Your view lists \
`ally_kills`: what each fellow Mafia has chosen so far tonight. Converging is not optional — \
a split night is a night the Town gets for free. Acting late lets you see and join; acting \
early sets the anchor others converge on. Both are legitimate; drifting alone is not.

2. **The Doctor may not shield the same seat two nights running.** Your view carries \
`cannot_protect` with the barred seat, or -1 when nothing is barred. Read it. A refused move \
costs you the night.

3. **A tied day vote eliminates NOBODY.** This arena does not hold a re-vote. So forcing a tie \
is a real way to save a suspect for another day, and Town must consolidate rather than split \
across two candidates.

## How to play well

**As Town.** Your information is the transcript. Track who accuses whom, who defends whom, \
and who stays quiet on the seats that matter — Mafia coordinate, and coordination leaves a \
pattern in voting even when it is absent from speech. Count the living. Every failed day vote \
brings the Mafia's parity closer, so a wrong elimination is worse than it looks. Do not follow \
a bandwagon you cannot justify from something actually said.

**As Detective.** A confirmed MAFIA finding is the strongest card in the game and it is worth \
only what you can convert. Claiming immediately makes you the next night's kill; sitting on it \
forever wastes it. Convert when your vote plus the claim can actually carry an elimination.

**As Doctor.** Protect the seat the Mafia most wants dead — usually whoever is driving Town's \
reads — not the seat you like most. Remember you cannot repeat, so plan two nights ahead.

**As Mafia.** Your risk is coordination showing. Do not all pile onto the same Town target in \
the same way; let one of you lead and the others follow softly, or stay off it entirely. Kill \
the seats that organise Town, not the loudest ones — noise is not the same as threat. Count \
to parity every single night and take the line that reaches it fastest.

**Everyone.** Say something with content. A message that commits to nothing gives your team \
nothing and reads as evasive to a table that is looking for exactly that.

## Your output

Call the `submit_action` tool with the action verb and its target seat. Use -1 as the target \
when the action names no seat. When the action is `message`, ALSO write the message body as \
your ordinary text alongside the tool call — the tool carries the verb, your text carries what \
the table hears. Do not answer with prose alone."""


class MafiaPolicy:
    game = GAME

    def system_prompt(self) -> str:
        return _SYSTEM

    # --- state rendering ------------------------------------------------------------

    def render_state(self, view: Any, memory: str) -> str:
        raw = getattr(view, "raw", {}) or {}
        seat = int(raw.get("your_seat", getattr(view, "seat", 0)) or 0)
        role = str(raw.get("your_role", "") or "")
        phase = str(raw.get("phase", getattr(view, "phase", "")) or "")
        day = int(raw.get("day", 0) or 0)
        legal = list(raw.get("legal", []) or [])
        alive = self._alive_seats(raw.get("alive"))
        allies = [int(a) for a in (raw.get("allies") or [])]

        lines = [
            f"DAY {day}, phase `{phase}`. You are seat {seat}, role {role or 'unknown'}.",
            f"Living seats ({len(alive)}): {alive}",
        ]

        # The parity count is the single most decision-relevant number in the game and
        # neither team is given it directly. Computed here for both teams identically.
        if allies:
            living_mafia = len([a for a in allies if a in alive]) + (1 if seat in alive else 0)
            living_town = len(alive) - living_mafia
            lines.append(
                f"Your Mafia allies: {allies}. "
                f"Count: {living_mafia} Mafia vs {living_town} Town — "
                + (
                    "you have reached parity and win."
                    if living_mafia >= living_town
                    else f"you need {living_town - living_mafia} more Town gone to reach parity."
                )
            )
        else:
            lines.append(
                "You have no `allies` field, which means you are TOWN. "
                f"{len(alive)} seats are alive; 3 of the original 12 were Mafia."
            )

        if role == "Doctor":
            barred = raw.get("cannot_protect", NO_TARGET)
            try:
                barred_i = int(barred)
            except (TypeError, ValueError):
                barred_i = NO_TARGET
            lines.append(
                f"You protected seat {barred_i} last night and may NOT protect them again tonight."
                if barred_i >= 0
                else "Nothing is barred tonight — you may protect any living seat, yourself included."
            )

        ally_kills = raw.get("ally_kills")
        if isinstance(ally_kills, dict) and ally_kills:
            picks = {int(k): int(v) for k, v in ally_kills.items()}
            lines.append(f"Ally kill picks so far tonight: {picks}")
            tally: dict[int, int] = {}
            for t in picks.values():
                tally[t] = tally.get(t, 0) + 1
            lead = sorted(tally.items(), key=lambda kv: (-kv[1], kv[0]))
            lines.append(
                f"  → current plurality: {lead}. Converge on the leader or the night is wasted."
            )
        elif role == "Mafia" and phase == "night":
            lines.append("No ally has picked yet tonight — your choice sets the anchor.")

        lines.append("")
        lines.append(self._transcript(raw.get("public") or []))
        priv = raw.get("private") or []
        if priv:
            lines.append("")
            lines.append("YOUR private results (never seen by anyone else):")
            for e in priv[-12:]:
                lines.append(f"  {self._event_line(e)}")

        if memory:
            lines.append("")
            lines.append("Your notes from earlier in this match:")
            lines.append(memory)

        lines.append("")
        lines.append(f"Actions legal for you right now: {legal}")
        if "message" in legal:
            lines.append(
                "Call `submit_action` with kind='message' and target=-1, and write the "
                "message body as your text."
            )
        else:
            lines.append("Call `submit_action` with the verb and the seat you are targeting.")
        return "\n".join(lines)

    def _alive_seats(self, alive: Any) -> list[int]:
        """`alive` arrives as {seat: bool} with string keys over JSON."""
        out: list[int] = []
        if isinstance(alive, dict):
            for k, v in alive.items():
                if v:
                    try:
                        out.append(int(k))
                    except (TypeError, ValueError):
                        continue
        elif isinstance(alive, list):
            for i, v in enumerate(alive):
                if v:
                    out.append(i)
        return sorted(out)

    def _transcript(self, public: list[Any]) -> str:
        if not public:
            return "Public transcript is empty so far."
        # Bounded to the most recent window. Mafia transcripts grow without limit and an
        # unbounded one would push the prompt past the cache prefix and re-bill the rules
        # every turn — the exact cost blow-up this harness is built to avoid.
        rows = ["Public transcript (most recent last):"]
        for e in public[-40:]:
            rows.append(f"  {self._event_line(e)}")
        return "\n".join(rows)

    def _event_line(self, e: Any) -> str:
        if not isinstance(e, dict):
            return str(e)
        t = e.get("type", "?")
        p = e.get("payload")
        if isinstance(p, dict):
            if t == "message":
                return f"[{t}] seat {p.get('from','?')} ({p.get('tone','')}): {p.get('text','')}"
            if t == "vote":
                return f"[{t}] seat {p.get('from','?')} voted seat {p.get('target','?')}"
            if t == "eliminate":
                return f"[{t}] seat {p.get('target','?')} eliminated ({p.get('cause','')})"
            if t == "night":
                return f"[{t}] {p}"
            return f"[{t}] {p}"
        return f"[{t}] {p}"

    # --- legality -------------------------------------------------------------------

    def _legal(self, view: Any) -> list[str]:
        raw = getattr(view, "raw", {}) or {}
        return [str(a) for a in (raw.get("legal") or [])]

    def _targets(self, view: Any) -> list[int]:
        raw = getattr(view, "raw", {}) or {}
        seat = int(raw.get("your_seat", 0) or 0)
        return [s for s in self._alive_seats(raw.get("alive")) if s != seat]

    def fallback(self, view: Any) -> dict[str, Any]:
        """A safe legal action: abstain where possible, otherwise the first legal verb.

        Deliberately inert. A fallback that voted for someone would inject a heuristic
        player's judgement into a match the report attributes to a model.
        """
        legal = self._legal(view)
        if not legal:
            return {"action": "abstain", "target": NO_TARGET}
        kind = legal[0]
        if kind == "message":
            return {"action": "message", "tone": "info", "text": "Still weighing this one."}
        targets = self._targets(view)
        return {"action": kind, "target": targets[0] if targets else NO_TARGET}

    def coerce(self, args: dict[str, Any], view: Any) -> tuple[dict[str, Any], str]:
        legal = self._legal(view)
        kind = str(args.get("kind", "") or "").strip().lower()
        source = "model"
        if kind not in legal:
            if not legal:
                return self.fallback(view), "repair"
            kind = legal[0]
            source = "repair"

        if kind == "message":
            text = str(args.get("_text", "") or "").strip()
            if not text:
                text = "Still weighing this one."
                source = "repair"
            return {"action": "message", "tone": "info", "text": text}, source

        raw_t = args.get("target", NO_TARGET)
        try:
            target = int(raw_t)
        except (TypeError, ValueError):
            target = NO_TARGET

        # Actions that name a seat must name a LIVING one that is not us. A dead or absent
        # target is a wasted decision, and repairing to a living seat keeps the match moving
        # while the repair counter records that the model got it wrong.
        if kind in ("vote", "night_kill", "investigate", "profile"):
            valid = self._targets(view)
            if target not in valid:
                if not valid:
                    return {"action": kind, "target": NO_TARGET}, "repair"
                target = valid[0]
                source = "repair"
        elif kind == "protect":
            raw = getattr(view, "raw", {}) or {}
            barred = raw.get("cannot_protect", NO_TARGET)
            try:
                barred_i = int(barred)
            except (TypeError, ValueError):
                barred_i = NO_TARGET
            # Self-protection is legal for the Doctor, so the valid set includes our own
            # seat — unlike the targeting actions above.
            valid = self._alive_seats(raw.get("alive"))
            valid = [s for s in valid if s != barred_i]
            if target not in valid:
                if not valid:
                    return {"action": kind, "target": NO_TARGET}, "repair"
                target = valid[0]
                source = "repair"

        return {"action": kind, "target": target}, source
