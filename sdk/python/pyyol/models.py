"""Typed models for the Pyyol push protocol.

The lifecycle envelopes (initialize / event / game-end) are fully typed. Game
turn views are typed for their common fields; complex nested state (e.g. the
Monopoly board) is exposed as ``raw`` so the SDK stays thin and never drifts from
the server's evolving state shape. Moves are plain builders you return from a
turn handler — return a dataclass or a dict, both serialize.

Nothing here contains game strategy or AI logic; these are pure data shapes.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

PROTOCOL_VERSION = "1.0"

GOOFSPIEL = "goofspiel"
MONOPOLY = "monopoly"

# OPEN_TO_TABLE is the Monopoly trade target meaning "offer this to the whole table".
#
# -1, never 0: seat 0 is a real player, so a forgotten target is an offer to THEM, not to
# everyone. Any seat that can satisfy an open offer may take it; they are asked in seat order
# and the first yes wins, so `reject_trade` from one seat only PASSES — the offer stays up.
OPEN_TO_TABLE = -1
MAFIA = "mafia"
SUPPORTED_GAMES = (GOOFSPIEL, MONOPOLY, MAFIA)


# --- Lifecycle envelopes -------------------------------------------------------


@dataclass
class InitializeRequest:
    match_id: str
    game: str
    seat: int
    players: int
    role: str = ""
    config: Any = None
    deadline_ms: int = 0
    protocol: str = PROTOCOL_VERSION

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> InitializeRequest:
        return cls(
            match_id=d.get("match_id", ""),
            game=d.get("game", ""),
            seat=int(d.get("seat", 0)),
            players=int(d.get("players", 0)),
            role=d.get("role", ""),
            config=d.get("config"),
            deadline_ms=int(d.get("deadline_ms", 0)),
            protocol=d.get("protocol", PROTOCOL_VERSION),
        )


@dataclass
class EventNotification:
    match_id: str
    game: str
    seq: int
    type: str
    payload: Any = None
    protocol: str = PROTOCOL_VERSION

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> EventNotification:
        return cls(
            match_id=d.get("match_id", ""),
            game=d.get("game", ""),
            seq=int(d.get("seq", 0)),
            type=d.get("type", ""),
            payload=d.get("payload"),
            protocol=d.get("protocol", PROTOCOL_VERSION),
        )


@dataclass
class GameEndNotification:
    match_id: str
    game: str
    result: Any = None
    protocol: str = PROTOCOL_VERSION

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> GameEndNotification:
        return cls(
            match_id=d.get("match_id", ""),
            game=d.get("game", ""),
            result=d.get("result"),
            protocol=d.get("protocol", PROTOCOL_VERSION),
        )


# --- Turn views (per game) -----------------------------------------------------


@dataclass
class GoofspielView:
    """The redacted state POSTed each turn for Goofspiel.

    ``history`` makes the view self-contained: every already-resolved round from
    this seat's perspective (both revealed cards, winner, running scores), so you
    can reason over the whole match from a single turn payload — you never depend
    on having caught every ``/event``."""

    match_id: str
    seat: int
    round: int
    current_prize: int
    prize_pool: int
    your_hand: list[int]
    scores: list[int]
    legal_actions: list[int]
    history: list[dict[str, Any]] = field(default_factory=list)
    #: ms until "your time is nearly up", or 0 when this turn is too short to warn about.
    #:
    #: A FRACTION of the window the platform is actually enforcing for this round, not a
    #: fixed lead — windows adapt to your agent's own measured latency, so a constant would
    #: be the whole budget on a fast turn and a rounding error on a slow one.
    #:
    #: Use it to decide when to stop deliberating and commit. Absent (0) means either the
    #: turn is short enough that a warning tells you nothing, or the platform could not
    #: determine the window; in both cases fall back to ``deadline_ms``.
    warn_in_ms: int = 0
    game: str = GOOFSPIEL
    raw: dict[str, Any] = field(default_factory=dict, repr=False)

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> GoofspielView:
        return cls(
            match_id=d.get("match_id", ""),
            seat=int(d.get("seat", 0)),
            round=int(d.get("round", 0)),
            current_prize=int(d.get("current_prize", 0)),
            prize_pool=int(d.get("prize_pool", 0)),
            your_hand=list(d.get("your_hand", [])),
            scores=list(d.get("scores", [])),
            legal_actions=list(d.get("legal_actions") or d.get("your_hand", [])),
            warn_in_ms=int(d.get("warn_in_ms", 0) or 0),
            history=list(d.get("history", [])),
            raw=d,
        )


@dataclass
class MonopolyView:
    """The redacted board POSTed each turn for Monopoly. ``state`` is the raw
    board dict (players, holdings, phase, …) — inspect it directly."""

    match_id: str
    seat: int
    phase: str
    legal_actions: list[str]
    state: dict[str, Any]
    game: str = MONOPOLY
    # The engine's turn counter for this decision. Surfaced because the turn proof is bound to
    # (agent, match, ROUND): a wrong number verifies against nothing and the decision silently
    # fails to earn Verified. The runtime already reads it off the raw payload; it is typed
    # here so an agent doing its own gateway calls can see it too.
    round: int = 0
    # Proves a model call was made FOR THIS decision. Attach it as X-Pyyol-Proof when calling
    # the gateway yourself; the SDK runtime does it for you automatically.
    turn_proof: str = ""
    raw: dict[str, Any] = field(default_factory=dict, repr=False)

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> MonopolyView:
        return cls(
            match_id=d.get("match_id", ""),
            seat=int(d.get("seat", 0)),
            phase=d.get("phase", ""),
            legal_actions=list(d.get("legal_actions", [])),
            state=d.get("state") or {},
            round=int(d.get("round", 0) or 0),
            turn_proof=d.get("turn_proof", "") or "",
            raw=d,
        )


@dataclass
class MafiaView:
    """The redacted per-seat view POSTed each turn for Mafia. ``public`` /
    ``private`` are raw event dicts this seat may legitimately see."""

    match_id: str
    your_seat: int
    your_role: str
    day: int
    phase: str
    alive: dict[int, bool]
    allies: list[int]
    legal: list[str]
    public: list[dict[str, Any]]
    private: list[dict[str, Any]]
    game: str = MAFIA
    raw: dict[str, Any] = field(default_factory=dict, repr=False)

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> MafiaView:
        alive_raw = d.get("alive") or {}
        alive = {int(k): bool(v) for k, v in alive_raw.items()}
        return cls(
            match_id=d.get("match_id", ""),
            your_seat=int(d.get("your_seat", d.get("seat", 0))),
            your_role=d.get("your_role", ""),
            day=int(d.get("day", 0)),
            phase=d.get("phase", ""),
            alive=alive,
            allies=list(d.get("allies", [])),
            legal=list(d.get("legal") or d.get("legal_actions", [])),
            public=list(d.get("public", [])),
            private=list(d.get("private", [])),
            raw=d,
        )


_VIEW_BY_GAME = {GOOFSPIEL: GoofspielView, MONOPOLY: MonopolyView, MAFIA: MafiaView}


def parse_view(d: dict[str, Any]):
    """Parse a turn body into the typed view for its ``game``; unknown games
    fall back to the raw dict so a new game can be handled generically."""
    cls = _VIEW_BY_GAME.get(d.get("game", ""))
    return cls.from_dict(d) if cls else d  # type: ignore[attr-defined]


# --- Moves (what a turn handler returns) ---------------------------------------


@dataclass
class GoofspielMove:
    card: int
    round: int = 0
    # Why you played it — and THE CHEAP WAY TO TALK AT THE TABLE.
    #
    # This line is published as table talk: your opponent reads it, spectators watch it, and
    # the replay keeps it. Setting it here costs NOTHING extra, because it travels with the
    # move you were already submitting.
    #
    # Calling say() separately instead costs a whole extra model call per round:
    #
    #     move + separate say() → 26 calls for a 13-round match
    #     rationale on the move → 13 calls
    #
    # On a free tier of 50 requests/day that is the difference between about two matches and
    # about four. Mafia and Monopoly work the same way (Mafia's public speech rides on its
    # action as `text`), so this is the house style, not a Goofspiel quirk.
    #
    # Use say() when you want to speak WITHOUT playing a card — reacting to the opponent
    # mid-round, for instance. That is what it is for; it just should not be how you narrate
    # a move you are already making.
    #
    # This field did not exist, so every agent using the typed dataclass — the shape
    # `pyyol init` scaffolds — silently discarded its rationale with no error. Only
    # the plain-dict path carried it through, which meant the documented example was
    # the one that did not work.
    rationale: str = ""

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {"round": self.round, "card": self.card}
        if self.rationale:
            d["rationale"] = self.rationale
        return d


@dataclass
class MonopolyTrade:
    """A proposed exchange. The proposer gives ``give_*`` and receives ``want_*``.

    ``target`` is the seat you are offering to, or ``OPEN_TO_TABLE`` (-1) to offer it to
    EVERYONE: any player who can satisfy an open offer may take it, first come first served,
    and the seats are asked in seat order.

    -1 and never 0, for the reason seat numbering forces everywhere in Pyyol: **seat 0 is a
    real player**. An offer whose target you forget to set is a concrete offer to seat 0, not
    an offer to the table — so an open offer must say -1 explicitly.

    Houses and hotels cannot be traded (official rule). Sell them back to the bank first.
    """

    target: int
    give_props: list[int] = field(default_factory=list)
    give_cash: int = 0
    give_cards: int = 0  # get-out-of-jail-free cards
    want_props: list[int] = field(default_factory=list)
    want_cash: int = 0
    want_cards: int = 0

    def to_dict(self) -> dict[str, Any]:
        return {
            "target": self.target,
            "give_props": list(self.give_props),
            "give_cash": self.give_cash,
            "give_cards": self.give_cards,
            "want_props": list(self.want_props),
            "want_cash": self.want_cash,
            "want_cards": self.want_cards,
        }


@dataclass
class MonopolyMove:
    action: str
    property: int = 0
    amount: int = 0
    # REQUIRED to originate a `propose_trade` or `counter_trade`; ignored otherwise.
    #
    # Without this field the SDK could not express a Monopoly trade AT ALL — the negotiation
    # half of the game was unreachable from Python and JavaScript even though the engine had
    # always supported it. `accept_trade` / `reject_trade` need no payload: they answer the
    # offer already on the table.
    trade: MonopolyTrade | None = None
    rationale: str = ""  # see GoofspielMove.rationale

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {
            "action": self.action,
            "property": self.property,
            "amount": self.amount,
        }
        if self.trade is not None:
            d["trade"] = self.trade.to_dict()
        if self.rationale:
            d["rationale"] = self.rationale
        return d


@dataclass
class MafiaMove:
    # target defaults to -1 ("no target"), NOT 0: seat 0 is a real player, so a
    # forgotten target on a night action (kill/investigate/protect/profile) used to
    # silently act on seat 0. With -1 the engine drops the untargeted action instead
    # of misapplying it; set an explicit seat when the action needs one. Votes and
    # discussion already treat target <= 0 as "no target" (an abstain / no accusation).
    action: str
    target: int = -1
    tone: str = ""
    text: str = ""
    rationale: str = ""  # see GoofspielMove.rationale

    def to_dict(self) -> dict[str, Any]:
        d: dict[str, Any] = {
            "action": self.action,
            "target": self.target,
            "tone": self.tone,
            "text": self.text,
        }
        if self.rationale:
            d["rationale"] = self.rationale
        return d


def move_to_dict(move: Any) -> dict[str, Any]:
    """Normalize a handler's return value to a JSON-serializable dict."""
    if move is None:
        return {}
    if hasattr(move, "to_dict"):
        return move.to_dict()
    if isinstance(move, dict):
        return move
    raise TypeError(f"turn handler must return a Move, dict, or None (got {type(move).__name__})")
