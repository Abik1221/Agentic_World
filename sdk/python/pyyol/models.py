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
from typing import Any, Dict, List

PROTOCOL_VERSION = "1.0"

GOOFSPIEL = "goofspiel"
MONOPOLY = "monopoly"
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
    def from_dict(cls, d: Dict[str, Any]) -> "InitializeRequest":
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
    def from_dict(cls, d: Dict[str, Any]) -> "EventNotification":
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
    def from_dict(cls, d: Dict[str, Any]) -> "GameEndNotification":
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
    your_hand: List[int]
    scores: List[int]
    legal_actions: List[int]
    history: List[Dict[str, Any]] = field(default_factory=list)
    game: str = GOOFSPIEL
    raw: Dict[str, Any] = field(default_factory=dict, repr=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "GoofspielView":
        return cls(
            match_id=d.get("match_id", ""),
            seat=int(d.get("seat", 0)),
            round=int(d.get("round", 0)),
            current_prize=int(d.get("current_prize", 0)),
            prize_pool=int(d.get("prize_pool", 0)),
            your_hand=list(d.get("your_hand", [])),
            scores=list(d.get("scores", [])),
            legal_actions=list(d.get("legal_actions") or d.get("your_hand", [])),
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
    legal_actions: List[str]
    state: Dict[str, Any]
    game: str = MONOPOLY
    raw: Dict[str, Any] = field(default_factory=dict, repr=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "MonopolyView":
        return cls(
            match_id=d.get("match_id", ""),
            seat=int(d.get("seat", 0)),
            phase=d.get("phase", ""),
            legal_actions=list(d.get("legal_actions", [])),
            state=d.get("state") or {},
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
    alive: Dict[int, bool]
    allies: List[int]
    legal: List[str]
    public: List[Dict[str, Any]]
    private: List[Dict[str, Any]]
    game: str = MAFIA
    raw: Dict[str, Any] = field(default_factory=dict, repr=False)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "MafiaView":
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


def parse_view(d: Dict[str, Any]):
    """Parse a turn body into the typed view for its ``game``; unknown games
    fall back to the raw dict so a new game can be handled generically."""
    cls = _VIEW_BY_GAME.get(d.get("game", ""))
    return cls.from_dict(d) if cls else d  # type: ignore[attr-defined]


# --- Moves (what a turn handler returns) ---------------------------------------


@dataclass
class GoofspielMove:
    card: int
    round: int = 0

    def to_dict(self) -> Dict[str, Any]:
        return {"round": self.round, "card": self.card}


@dataclass
class MonopolyMove:
    action: str
    property: int = 0
    amount: int = 0

    def to_dict(self) -> Dict[str, Any]:
        return {"action": self.action, "property": self.property, "amount": self.amount}


@dataclass
class MafiaMove:
    action: str
    target: int = 0
    tone: str = ""
    text: str = ""

    def to_dict(self) -> Dict[str, Any]:
        return {"action": self.action, "target": self.target, "tone": self.tone, "text": self.text}


def move_to_dict(move: Any) -> Dict[str, Any]:
    """Normalize a handler's return value to a JSON-serializable dict."""
    if move is None:
        return {}
    if hasattr(move, "to_dict"):
        return move.to_dict()
    if isinstance(move, dict):
        return move
    raise TypeError(f"turn handler must return a Move, dict, or None (got {type(move).__name__})")
