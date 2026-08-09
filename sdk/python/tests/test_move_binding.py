"""Cross-language conformance for COMPLETION BINDING, driven by shared fixtures.

The expectations live in ``sdk/conformance/move_binding.json`` and are read by this suite, the
JS SDK's ``move-binding.test.ts`` and the Go gateway's ``internal/movebind/conformance_test.go``.

The point is drift. Three implementations of this reduction exist and nothing forces them to
agree. And a divergence here does not surface as a visible bug — it surfaces as an HONEST TURN
BEING REJECTED, because the gateway reduced the model's answer one way and the match compared
it against the same move reduced another way. Sharing one set of expectations makes that a
failing test instead of a support ticket.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

from pyyol import movetools

_FIXTURES = Path(__file__).resolve().parents[2] / "conformance" / "move_binding.json"


def _load() -> dict[str, Any]:
    # A missing fixture file must fail loudly rather than skip: a conformance suite that finds
    # nothing and reports "passed" is exactly the drift it exists to catch.
    assert _FIXTURES.is_file(), f"move-binding fixtures not found at {_FIXTURES}"
    return json.loads(_FIXTURES.read_text())


_DOC = _load()
_CASES = _DOC["cases"]
assert _CASES, "move_binding.json contains no cases"


@pytest.mark.parametrize("case", _CASES, ids=[c["name"] for c in _CASES])
def test_move_binding_conformance(case: dict[str, Any]) -> None:
    game, tool, expect = case["game"], case["tool"], case["expect_move"]

    # The fixture names the tool AND the game, so this also pins that the two agree. A game
    # whose tool name disagreed would mean the SDK sends a tool the gateway does not look for,
    # and every move would silently go unbound.
    assert movetools.tool_name(game) == tool, (
        f"tool_name({game!r}) = {movetools.tool_name(game)!r}, fixture expects {tool!r}"
    )

    got = movetools.bound_move(game, case["response"])
    if expect is None:
        assert got is None, (
            f"bound {got!r} from a response that must bind NOTHING.\nwhy: {case['why']}"
        )
        return
    assert got == expect, f"bound {got!r}, want {expect!r}.\nwhy: {case['why']}"


def test_tool_definitions_carry_the_right_name_per_provider() -> None:
    """The envelope differs per provider; the tool NAME must not.

    A provider-specific wrapper that renamed the tool would produce calls the gateway ignores,
    so the agent would look instrumented and bind nothing.
    """
    for game in (movetools.GAME_GOOFSPIEL, movetools.GAME_MAFIA, movetools.GAME_MONOPOLY):
        want = movetools.tool_name(game)
        openai = movetools.tool_for(game, "openai")
        assert openai["function"]["name"] == want
        assert openai["type"] == "function"
        assert movetools.tool_for(game, "anthropic")["name"] == want
        assert movetools.tool_for(game, "google")["name"] == want
        # The JSON Schema itself must be the SAME object in every envelope, or a model told one
        # thing by one provider and another by the next would emit inconsistent arguments.
        assert (
            openai["function"]["parameters"]
            == movetools.tool_for(game, "anthropic")["input_schema"]
            == movetools.tool_for(game, "google")["parameters"]
        )


def test_tool_choice_requires_the_move_tool() -> None:
    # tool_choice is what turns "the model may call this" into "the model must", and a turn
    # with no tool call earns no binding at all.
    assert movetools.tool_choice_for("goofspiel", "anthropic") == {
        "type": "tool",
        "name": "play_card",
    }
    assert movetools.tool_choice_for("goofspiel", "openai") == {
        "type": "function",
        "function": {"name": "play_card"},
    }
    google = movetools.tool_choice_for("goofspiel", "google")
    assert google["function_calling_config"]["allowed_function_names"] == ["play_card"]


def test_unknown_game_has_no_tool_and_raises_rather_than_guessing() -> None:
    assert movetools.tool_name("chess") == ""
    with pytest.raises(ValueError):
        movetools.tool_for("chess")


def test_no_target_is_minus_one_not_zero() -> None:
    """The one constant worth asserting directly: seat 0 is a real player.

    If NO_TARGET were 0, every untargeted action would bind as an action against that player,
    and the honest untargeted move that followed would be rejected.
    """
    assert movetools.NO_TARGET == -1
    assert movetools.canon_mafia("vote", -1) == "vote:none"
    assert movetools.canon_mafia("vote", 0) == "vote:0"
    assert movetools.canon_mafia("vote", -1) != movetools.canon_mafia("vote", 0)


def test_canon_monopoly_cannot_confuse_property_with_amount() -> None:
    assert movetools.canon_monopoly("mortgage", 50, 0) != movetools.canon_monopoly(
        "mortgage", 0, 50
    )


def test_sdk_objects_are_read_like_dicts() -> None:
    """Provider SDKs return objects, not dicts, and the fixtures are dicts.

    Without this the conformance suite would pass against dict fixtures while the real
    Anthropic/OpenAI client objects bound nothing — the fixture and reality diverging in the
    one direction the fixture cannot see.
    """

    class Block:
        type = "tool_use"
        name = "play_card"
        input = {"card": 7}  # noqa: A003

    class Resp:
        content = [Block()]

    assert movetools.bound_move("goofspiel", Resp()) == "card:7"
