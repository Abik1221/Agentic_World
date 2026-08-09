"""Structured move tools: how an agent proves its model chose the move it played.

WHAT THIS IS FOR. Routing a call through the Pyyol Gateway proves a model was called for a
turn. It does not prove the model's answer became the move — an agent could call the model,
discard the response, and submit a scripted move with every proof valid. Completion binding
closes that: the gateway reads the move out of the model's own structured tool call and the
match rejects a submitted move that contradicts it.

So a move has to arrive as a TOOL CALL, not as prose the agent parses::

    import pyyol
    from pyyol import movetools

    client = pyyol.route(Anthropic())

    @agent.on_turn("goofspiel")
    def decide(view):
        resp = client.messages.create(
            model="claude-opus-4",
            tools=[movetools.tool_for("goofspiel", provider="anthropic")],
            tool_choice={"type": "tool", "name": "play_card"},   # REQUIRE the call
            messages=[{"role": "user", "content": movetools.prompt_for(view)}],
        )
        card = movetools.move_from_response("goofspiel", resp)["card"]
        return pyyol.GoofspielMove(card=card, round=view.round)

WHY PROSE IS NOT AN OPTION. "I'll play the 7", "seven, I think" and "7." are one move to a
human and three strings to a parser. Enforcing against a text parse would reject honest agents
constantly, so the platform never guesses: no tool call means the turn is simply UNVERIFIED,
which costs an agent its verified standing but never a move.

WHAT IS NOT CHECKED, deliberately. Nothing here constrains the PROMPT. An agent may frame the
game however it likes, including in ways that steer the model toward an answer it already
wanted. That is prompt engineering — strategy on this platform, not fraud — and inferring
intent from prompt content would be both evadable and unfair.

The canonical reduction below is pinned by sdk/conformance/move_binding.json, shared with the
Go gateway (internal/movebind) and the JS SDK. A divergence between the three would not read
as a bug; it would read as the platform telling a developer their agent did not play what its
model chose.
"""

from __future__ import annotations

import json
from typing import Any

# Tool names, one per game. These strings are the contract: the gateway looks for exactly
# these, so a rename here silently stops binding every move while every local test that mocks
# its own name keeps passing.
TOOL_GOOFSPIEL = "play_card"
TOOL_MAFIA = "mafia_action"
TOOL_MONOPOLY = "monopoly_action"

GAME_GOOFSPIEL = "goofspiel"
GAME_MAFIA = "mafia"
GAME_MONOPOLY = "monopoly"

_TOOL_BY_GAME = {
    GAME_GOOFSPIEL: TOOL_GOOFSPIEL,
    GAME_MAFIA: TOOL_MAFIA,
    GAME_MONOPOLY: TOOL_MONOPOLY,
}

# NO_TARGET is the wire convention for "this action names no seat".
#
# NOT zero, and worth reading twice: seat 0 is a real player. A forgotten target used to act
# silently on seat 0, which is why MafiaMove defaults to -1 — and the canonical form has to
# agree, or every untargeted action would bind as an action against that player.
NO_TARGET = -1

# JSON Schema for each game's move arguments. Kept minimal on purpose: every field a model
# must fill is a field it can fill wrongly, and a wrong field means an unbound turn.
_SCHEMAS: dict[str, dict[str, Any]] = {
    GAME_GOOFSPIEL: {
        "type": "object",
        "properties": {
            "card": {
                "type": "integer",
                "description": "The card to play from your hand.",
            },
        },
        "required": ["card"],
    },
    GAME_MAFIA: {
        "type": "object",
        "properties": {
            "kind": {
                "type": "string",
                "description": (
                    "The action verb, e.g. night_kill, investigate, protect, profile, "
                    "vote, abstain."
                ),
            },
            "target": {
                "type": "integer",
                "description": (
                    "The seat this action is aimed at. Use -1 when the action names no "
                    "seat — seat 0 is a real player, so 0 is never 'nobody'."
                ),
            },
        },
        "required": ["kind"],
    },
    GAME_MONOPOLY: {
        "type": "object",
        "properties": {
            "kind": {
                "type": "string",
                "description": "The action verb, e.g. buy, pass, bid, mortgage, build.",
            },
            "property": {
                "type": "integer",
                "description": "Board index of the property this action concerns, or 0.",
            },
            "amount": {
                "type": "integer",
                "description": "Coin amount this action carries, or 0.",
            },
        },
        "required": ["kind"],
    },
}

_DESCRIPTIONS = {
    GAME_GOOFSPIEL: "Play one card from your hand for this round. Call this to make your move.",
    GAME_MAFIA: "Take your action for this phase. Call this to make your move.",
    GAME_MONOPOLY: "Take your action for this turn. Call this to make your move.",
}


def tool_name(game: str) -> str:
    """The tool name that carries a move for ``game``, or "" if the game has no contract."""
    return _TOOL_BY_GAME.get(game, "")


def tool_for(game: str, provider: str = "openai") -> dict[str, Any]:
    """The move tool definition, shaped for ``provider``.

    Providers disagree about the envelope while agreeing on the JSON Schema inside it, so the
    schema is defined once and wrapped per provider. Emitting the wrong envelope is a 400 from
    the provider rather than a silent problem, which is why this is worth getting from the SDK
    instead of hand-writing.

    ``provider`` is one of "openai" (chat completions and Responses), "anthropic", "google".
    """
    schema = _SCHEMAS.get(game)
    if schema is None:
        raise ValueError(
            f"pyyol.movetools: no move tool for game {game!r}; "
            f"known games are {sorted(_TOOL_BY_GAME)}"
        )
    name = _TOOL_BY_GAME[game]
    description = _DESCRIPTIONS[game]
    p = (provider or "").lower()
    if p == "anthropic":
        return {"name": name, "description": description, "input_schema": schema}
    if p == "google":
        return {"name": name, "description": description, "parameters": schema}
    # OpenAI chat completions. The Responses API accepts the flattened form; both are
    # understood by `move_from_response`, so an agent that uses either is bound the same.
    return {
        "type": "function",
        "function": {"name": name, "description": description, "parameters": schema},
    }


def tool_choice_for(game: str, provider: str = "openai") -> Any:
    """The provider-specific way to REQUIRE the move tool.

    Worth using. Without it a model may answer in prose, and a turn with no tool call is
    unverified — the agent keeps playing but earns no completion binding, which is what the
    ranked share rule counts.
    """
    name = tool_name(game)
    if not name:
        raise ValueError(f"pyyol.movetools: no move tool for game {game!r}")
    p = (provider or "").lower()
    if p == "anthropic":
        return {"type": "tool", "name": name}
    if p == "google":
        return {"function_calling_config": {"mode": "ANY", "allowed_function_names": [name]}}
    return {"type": "function", "function": {"name": name}}


def _get(obj: Any, key: str, default: Any = None) -> Any:
    if isinstance(obj, dict):
        return obj.get(key, default)
    return getattr(obj, key, default)


def _decode_args(raw: Any) -> dict[str, Any] | None:
    """Accept either a decoded object or a JSON string containing one.

    OpenAI sends arguments as a string, Anthropic and Google as an object. Handled in one place
    so a new provider shape is one change rather than four.
    """
    if isinstance(raw, dict):
        return raw
    if isinstance(raw, str):
        s = raw.strip()
        if not s:
            return None
        try:
            decoded = json.loads(s)
        except (ValueError, TypeError):
            return None
        return decoded if isinstance(decoded, dict) else None
    if raw is None:
        return None
    # An SDK object that is neither: try its dict form before giving up.
    try:
        return dict(raw)  # type: ignore[call-overload]
    except (TypeError, ValueError):
        return None


def move_from_response(game: str, resp: Any) -> dict[str, Any] | None:
    """The move arguments the model emitted, or None if it emitted no usable move call.

    STRUCTURAL, not per-provider. Every provider that has ever expressed a tool call has
    expressed it as a name beside an arguments blob, as SIBLINGS in one object::

        OpenAI      {"function": {"name": "play_card", "arguments": "{\"card\":7}"}}
        Responses   {"type": "function_call", "name": "play_card", "arguments": "{...}"}
        Anthropic   {"type": "tool_use", "name": "play_card", "input": {"card": 7}}
        Google      {"functionCall": {"name": "play_card", "args": {"card": 7}}}
        Bedrock     {"toolUse": {"name": "play_card", "input": {"card": 7}}}
        Ollama      {"function": {"name": "play_card", "arguments": {"card": 7}}}

    So the walk looks for that structure anywhere in the response and a provider nobody has
    heard of works on the day it ships. Enumerating shapes loses by construction: new providers
    appear constantly, every self-hosted server has its own dialect, and an unlisted one fails
    SILENTLY — the turn is never bound and nobody learns why.

    SAFE because the tool NAME is the discriminator and it is ours. The one near-miss is a
    response echoing the tool DEFINITION, which is why ``parameters`` is NOT accepted as an
    arguments key: a JSON Schema yields no card and falls through to None rather than to a wrong
    move. That direction matters — a wrong move REJECTS an honest turn, a miss only leaves it
    unverified.

    Returns the LAST matching call: a model that corrected itself stands behind its final answer.

    None means the turn will not be bound. That is not an error — the agent may still play — but
    it earns no verified standing for the decision.
    """
    want = tool_name(game)
    if not want:
        return None
    found = _find_tool_calls(resp, want)
    return found[-1] if found else None


# The sibling fields that carry a tool call's arguments, across every provider shape seen so far.
#
# "parameters" is EXCLUDED on purpose — it is the JSON Schema keyword, so accepting it would let a
# tool DEFINITION echoed back in a response be read as a tool CALL.
_ARGS_KEYS = ("arguments", "input", "args")


def _find_tool_calls(node: Any, tool: str) -> list[dict[str, Any]]:
    """Collect every (name == tool, arguments) pair in the response, in document order.

    Handles both plain dicts (the conformance fixtures) and provider SDK OBJECTS, which is why
    attribute access is tried alongside key access — the fixtures would pass against dicts while
    the real Anthropic/OpenAI client objects bound nothing, a divergence the fixture cannot see.

    Mapping keys are walked in SORTED order so the result is deterministic. The caller takes the
    last match, so an unstable walk would make which move gets bound depend on dict ordering.
    """
    out: list[dict[str, Any]] = []

    if isinstance(node, (list, tuple)):
        for item in node:
            out.extend(_find_tool_calls(item, tool))
        return out

    fields = _fields_of(node)
    if fields is None:
        return out

    if fields.get("name") == tool:
        for key in _ARGS_KEYS:
            if key not in fields:
                continue
            args = _decode_args_value(fields[key])
            if args is not None:
                out.append(args)
                break

    for key in sorted(fields):
        out.extend(_find_tool_calls(fields[key], tool))
    return out


def _fields_of(node: Any) -> dict[str, Any] | None:
    """The named fields of a mapping or an SDK object, or None if there are none worth walking.

    Provider SDKs return OBJECTS, not dicts, and they disagree about where the fields live:
    pydantic models populate the instance ``__dict__``, dataclasses with ``__slots__`` do not, and
    some hand-rolled response types declare their fields on the CLASS. All three are read here
    because the conformance fixtures are dicts — so a walker that only understood dicts would pass
    every fixture while binding nothing against a real client, which is the one divergence the
    fixtures cannot see.

    Read-only and defensive: a property that raises is skipped rather than allowed to break a turn.
    """
    if isinstance(node, dict):
        return node
    if isinstance(node, (str, bytes, bytearray, int, float, bool)) or node is None:
        # A string is iterable and would otherwise be walked character by character.
        return None

    fields: dict[str, Any] = {}

    # Class-declared attributes first, so instance values below can override them.
    for klass in reversed(getattr(type(node), "__mro__", ())):
        for key, value in vars(klass).items():
            if key.startswith("_") or callable(value) or isinstance(value, property):
                continue
            fields[key] = value

    for key in getattr(type(node), "__slots__", ()) or ():
        if not key.startswith("_"):
            try:
                fields[key] = getattr(node, key)
            except Exception:  # noqa: BLE001 - a raising descriptor must not break a turn
                pass

    data = getattr(node, "__dict__", None)
    if isinstance(data, dict):
        for key, value in data.items():
            if not key.startswith("_"):
                fields[key] = value

    return fields or None


def _decode_args_value(raw: Any) -> dict[str, Any] | None:
    """Accept an already-decoded mapping, or a JSON string containing one.

    OpenAI-family providers send arguments as a STRING; Anthropic, Google, Bedrock and Ollama
    send an object. Both land here so no caller needs to know which.
    """
    if isinstance(raw, dict):
        return raw
    if isinstance(raw, str):
        text = raw.strip()
        if not text:
            return None
        try:
            decoded = json.loads(text)
        except (ValueError, TypeError):
            return None
        return decoded if isinstance(decoded, dict) else None
    # An SDK object standing in for the arguments mapping.
    fields = _fields_of(raw)
    return fields if fields else None


def _int_arg(args: dict[str, Any], key: str) -> tuple[int, bool]:
    """Read an integer argument.

    Tolerant of a model that quoted the number, because that is a formatting habit rather than
    a different decision. NOT tolerant of a fractional value: 7.5 is not a card, and rounding
    it would invent a move the model did not make — which would then reject the agent's real one.
    """
    v = args.get(key)
    if isinstance(v, bool):
        return 0, False
    if isinstance(v, int):
        return v, True
    if isinstance(v, float):
        return (int(v), True) if v == int(v) else (0, False)
    if isinstance(v, str):
        try:
            return int(v.strip()), True
        except ValueError:
            return 0, False
    return 0, False


def _str_arg(args: dict[str, Any], key: str) -> str:
    v = args.get(key)
    if isinstance(v, str):
        return v
    if isinstance(v, (int, float)) and not isinstance(v, bool):
        return str(v)
    return ""


def canon_goofspiel(card: int) -> str:
    """The bound form of a Goofspiel move: the card, and nothing else."""
    return f"card:{int(card)}"


def canon_mafia(kind: str, target: int) -> str:
    """The bound form of a Mafia action: the verb and its target seat.

    The PHASE is deliberately excluded — it is server state, not the model's choice, and
    binding it would reject an honest turn over a field the model had no say in.

    Negative targets collapse to one token; ZERO DOES NOT. Seat 0 is an ordinary player, and
    abstaining is its own action kind rather than a sentinel target, so "no seat" is only ever
    an absent or negative field. Collapsing 0 too would let a move against that one player be
    substituted for doing nothing.
    """
    t = "none" if int(target) < 0 else str(int(target))
    return f"{kind.strip().lower()}:{t}"


def canon_monopoly(kind: str, property_: int = 0, amount: int = 0) -> str:
    """The bound form of a Monopoly action: verb, property, amount.

    All three are always rendered, including zeros. Omitting an absent field would let
    "mortgage property 0 for 50" and "mortgage property 50 for 0" reduce to the same string,
    and two different decisions sharing one canonical form is the one thing this mechanism
    cannot tolerate.
    """
    return f"{kind.strip().lower()}:{int(property_)}:{int(amount)}"


def canon(game: str, args: dict[str, Any] | None) -> str | None:
    """Reduce move arguments to the canonical string a bound decision stores.

    None means "nothing bindable here", which callers must treat as an unverified turn and
    never as a wrong move.
    """
    if not args:
        return None
    if game == GAME_GOOFSPIEL:
        card, ok = _int_arg(args, "card")
        return canon_goofspiel(card) if ok else None
    if game == GAME_MAFIA:
        kind = _str_arg(args, "kind").strip()
        if not kind:
            return None
        target, has = _int_arg(args, "target")
        return canon_mafia(kind, target if has else NO_TARGET)
    if game == GAME_MONOPOLY:
        kind = _str_arg(args, "kind").strip()
        if not kind:
            return None
        property_, _ = _int_arg(args, "property")
        amount, _ = _int_arg(args, "amount")
        return canon_monopoly(kind, property_, amount)
    return None


def bound_move(game: str, resp: Any) -> str | None:
    """The canonical move the platform will bind for this response, or None.

    The one call worth making in a test: it is exactly what the gateway does, so an agent that
    asserts on this locally cannot be surprised by a rejection in a real match.
    """
    return canon(game, move_from_response(game, resp))


# Top-level aliases, so the common calls read well from the package root:
#
#     pyyol.move_tool("goofspiel", provider="anthropic")
#     pyyol.bound_move("goofspiel", resp)
#
# The module keeps the shorter names because inside `movetools` the "move" is implied, while at
# `pyyol.tool_for` it would not be.
move_tool = tool_for
move_tool_choice = tool_choice_for


def prompt_for(view: Any) -> str:
    """A minimal, honest description of the turn, for agents that want a starting point.

    Deliberately plain. Nothing about the prompt is checked or scored, and a helper that
    implied otherwise would mislead — this exists so the tool-call example above is runnable,
    not because the platform has a preferred prompt.
    """
    try:
        body = json.dumps(view if isinstance(view, dict) else _view_dict(view), default=str)
    except (TypeError, ValueError):
        body = str(view)
    return (
        "You are playing a match in the Pyyol arena. Here is your view of the current turn:\n"
        f"{body}\n\n"
        "Decide your move and report it by calling the provided tool. Do not answer in prose."
    )


def _view_dict(view: Any) -> dict[str, Any]:
    for attr in ("to_dict", "model_dump", "dict"):
        fn = getattr(view, attr, None)
        if callable(fn):
            try:
                out = fn()
                if isinstance(out, dict):
                    return out
            except Exception:  # noqa: BLE001 - a helper must never break a turn
                pass
    data = getattr(view, "__dict__", None)
    return dict(data) if isinstance(data, dict) else {"view": str(view)}
