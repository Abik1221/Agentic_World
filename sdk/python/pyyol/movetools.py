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
                "description": (
                    "Board index of the property this action concerns, or 0. On a bid during "
                    "a HOUSING SHORTAGE auction this is the square you would put the piece on."
                ),
            },
            "amount": {
                "type": "integer",
                "description": "Coin amount this action carries, or 0.",
            },
            # The trade payload. OPTIONAL and NOT part of the canonical bound form — a trade
            # binds on its verb alone (a nested structure re-rendered cosmetically differently
            # would reject an honest turn), so nothing here can cost a turn its binding.
            # Without it a bound agent could act but never DEAL, which is most of Monopoly.
            "trade": {
                "type": "object",
                "description": "Required to propose or counter a trade. Ignored for other actions.",
                "properties": {
                    "target": {
                        "type": "integer",
                        "description": (
                            "Seat to offer to, or -1 to offer to the WHOLE TABLE (any player "
                            "who can satisfy it may take it). Never 0 for 'everyone' — seat 0 "
                            "is a real player."
                        ),
                    },
                    "give_props": {"type": "array", "items": {"type": "integer"}, "description": "Squares you give."},
                    "give_cash": {"type": "integer", "description": "Cash you give."},
                    "give_cards": {"type": "integer", "description": "Get-out-of-jail-free cards you give."},
                    "want_props": {"type": "array", "items": {"type": "integer"}, "description": "Squares you want."},
                    "want_cash": {"type": "integer", "description": "Cash you want."},
                    "want_cards": {"type": "integer", "description": "Get-out-of-jail-free cards you want."},
                },
                "required": ["target"],
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


def _plan_schema(game: str, base: dict[str, Any], rounds: int) -> dict[str, Any]:
    """The batching form of a move schema: a plan of per-round moves.

    A SEPARATE schema rather than an optional ``plan`` property beside ``card``, because a
    schema that accepts either shape has to drop ``required``, and a model handed an
    all-optional object will sometimes return an empty one. Anthropic's and OpenAI's strict
    modes are also unenthusiastic about ``oneOf``. So an agent that batches asks for the plan
    tool and is told exactly one shape; an agent that does not gets today's schema untouched.
    """
    entry = {
        "type": "object",
        "properties": {
            "round": {
                "type": "integer",
                "description": (
                    "The round this move is for. Must be the current round or a later one — "
                    "a move for a round already played cannot be bound."
                ),
            },
            **base["properties"],
        },
        "required": ["round", *base.get("required", [])],
    }
    return {
        "type": "object",
        "properties": {
            PLAN_KEY: {
                "type": "array",
                "minItems": 1,
                "maxItems": min(rounds, MAX_SPAN_ROUNDS),
                "description": (
                    "One entry per round you are deciding now, starting at the current round. "
                    "Every round you list is bound to the move you give it, so list only "
                    "rounds you intend to play exactly as planned."
                ),
                "items": entry,
            },
        },
        "required": [PLAN_KEY],
    }


def tool_for(game: str, provider: str = "openai", plan_rounds: int | None = None) -> dict[str, Any]:
    """The move tool definition, shaped for ``provider``.

    Providers disagree about the envelope while agreeing on the JSON Schema inside it, so the
    schema is defined once and wrapped per provider. Emitting the wrong envelope is a 400 from
    the provider rather than a silent problem, which is why this is worth getting from the SDK
    instead of hand-writing.

    ``provider`` is one of "openai" (chat completions and Responses), "anthropic", "google".

    ``plan_rounds`` asks for the BATCHING form: one call that decides up to that many rounds.
    Leave it None for the ordinary one-move-per-call tool, which is what every agent shipping
    today uses and is completely unchanged.

    Batching is cost optimisation and this platform means to reward it. Coverage counts the
    DECISIONS a model made rather than the calls made, so a plan of three rounds is three
    verified decisions — but each of them is BINDING. Submitting a different move for a round
    you planned is rejected exactly as a substitution is, so plan only what you mean to play.
    """
    schema = _SCHEMAS.get(game)
    if schema is None:
        raise ValueError(
            f"pyyol.movetools: no move tool for game {game!r}; "
            f"known games are {sorted(_TOOL_BY_GAME)}"
        )
    if plan_rounds is not None:
        if plan_rounds < 1:
            raise ValueError("pyyol.movetools: plan_rounds must be at least 1")
        schema = _plan_schema(game, schema, plan_rounds)
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
    """The tool_choice value that FORCES the model to answer with the move tool.

    NOT every model accepts forcing. Some advertise tool support and still reject a
    required/named tool_choice — observed live: OpenRouter's ``openai/gpt-oss-20b:free``
    answers ``inference-enforced tool_choice (required/named) is not supported``, HTTP 400, on
    every call.

    If you see that, send ``"auto"`` instead. Binding reads the RESPONSE, so forcing is only a
    way to raise the hit rate — a model that emits the tool call on its own binds exactly the
    same. Forcing is the default because most models take it and it wastes fewer turns.
    """
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
        return dict(raw)
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


# The argument that carries a multi-round decision. Named once, and it has to match the Go
# gateway and the JS SDK exactly: a mismatch would not error, it would silently fall back to
# single-round binding and quietly restore the coverage problem this exists to fix.
PLAN_KEY = "plan"

# The most rounds one completion may claim to have decided.
#
# Bounded because the plan is attacker-supplied — without a cap a single call could assert a
# hundred thousand rounds and become that many database writes. Comfortably above any real
# game, so a legitimate agent never meets it.
MAX_SPAN_ROUNDS = 64


def canon_plan(
    game: str, args: dict[str, Any] | None, proven_round: int
) -> list[dict[str, Any]] | None:
    """Reduce move arguments to EVERY round they decided, as ``[{"round": n, "move": s}]``.

    # Why a completion may cover more than one round

    Coverage used to count CALLS, so one completion bound one round. An agent that batches —
    one call planning three rounds — therefore scored about 33% on real staked tables while
    playing entirely model-backed, and cost optimisation is something this platform means to
    REWARD. Coverage now means "decisions a model made" rather than "calls made".

    # Why claiming a span is safe

    A span is a COMMITMENT, not a free coverage win. Match-time enforcement is unchanged, so
    submitting anything other than the bound move for a covered round is rejected exactly as a
    substitution is. An agent that over-claims has only tied its own hands.

    # The one thing a span must never do

    Rounds before ``proven_round`` are DROPPED. Those turns have already been played, so a
    binding over them is coverage nothing will ever check — an agent could retroactively claim
    turns it played unbound. Forward claims are self-limiting because they are enforced.

    None means nothing is bindable, which callers must treat as an unverified turn and never as
    a wrong move. A plan naming one round twice returns None WHOLE: two moves for one slot has
    no honest reading, and picking either would be guessing on the agent's behalf.
    """
    if not args:
        return None
    entries = _plan_entries(args)
    if entries is None:
        # No plan: the ordinary single-round call, unchanged.
        move = canon(game, args)
        return [{"round": proven_round, "move": move}] if move else None
    if len(entries) > MAX_SPAN_ROUNDS:
        return None
    seen: set[int] = set()
    out: list[dict[str, Any]] = []
    for entry in entries:
        rnd, has = _int_arg(entry, "round")
        if not has or rnd < proven_round:
            # Backward or unplaceable: skipped, never fatal. A model that emitted one bad
            # entry has still honestly decided the others.
            continue
        if rnd in seen:
            return None
        move = canon(game, entry)
        if not move:
            continue
        seen.add(rnd)
        out.append({"round": rnd, "move": move})
    if not out:
        return None
    out.sort(key=lambda rm: rm["round"])
    return out


def _plan_entries(args: dict[str, Any]) -> list[dict[str, Any]] | None:
    """The per-round argument objects in a plan, or None when this call carries no plan.

    None must mean "no plan" rather than "empty plan", because the caller falls back to
    single-round binding on None and binding nothing would break every agent shipping today.
    """
    raw = args.get(PLAN_KEY)
    if not isinstance(raw, list) or not raw:
        return None
    out = [item for item in raw if isinstance(item, dict)]
    return out or None


def bound_plan(game: str, resp: Any, proven_round: int) -> list[dict[str, Any]] | None:
    """Every round this response will bind, exactly as the gateway will read it.

    Use it in a test before shipping a batching agent: if this does not list the round you are
    about to play, that turn will not be bound, and if it lists a DIFFERENT move than you
    intend to submit, the match will reject it.
    """
    return canon_plan(game, move_from_response(game, resp), proven_round)


# Top-level aliases, so the common calls read well from the package root:
#
#     pyyol.move_tool("goofspiel", provider="anthropic")
#     pyyol.bound_move("goofspiel", resp)
#
# The module keeps the shorter names because inside `movetools` the "move" is implied, while at
# `pyyol.tool_for` it would not be.
move_tool = tool_for
move_tool_choice = tool_choice_for
# Same reasoning, and the same names the JS SDK uses. These were the only movetools symbols a
# developer could not find by porting `pyyol.X` from one SDK to the other: the logic was here all
# along under a shorter in-module name, so the gap was purely in what the package exported.
move_tool_name = tool_name
canon_move = canon


def prompt_for(view: Any) -> str:
    """A minimal, honest description of the turn, for agents that want a starting point.

    Deliberately plain. Nothing about the prompt is checked or scored, and a helper that
    implied otherwise would mislead — this exists so the tool-call example above is runnable,
    not because the platform has a preferred prompt.

    Compact separators and ensure_ascii=False, deliberately — both to match JS. json.dumps
    defaults to ", "/": " where JSON.stringify emits neither, AND it escapes non-ASCII to
    \\uXXXX where JSON.stringify emits raw UTF-8. So the two SDKs built DIFFERENT prompts from
    the same view, and the second difference fires on any view carrying a non-English handle or
    chat line — i.e. exactly the matches this arena runs. "The SDKs are equivalent" quietly
    stops being true, and a paired comparison of one scaffold across languages ends up comparing
    two different prompts. Both forms are also fewer tokens.

    sdk/conformance/prompt_for.json pins both languages to the same string, unicode case
    included; it is read by this SDK's tests and the JS SDK's.
    """
    try:
        body = json.dumps(
            view if isinstance(view, dict) else _view_dict(view),
            separators=(",", ":"),
            ensure_ascii=False,
            default=str,
        )
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
