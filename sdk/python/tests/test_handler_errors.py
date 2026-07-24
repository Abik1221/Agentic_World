"""A crashing turn handler must be surfaced (error + real message), not swallowed."""

from __future__ import annotations

from pyyol import Agent


def test_handler_error_returns_real_message():
    a = Agent(supported_games=["goofspiel"], name="t")

    @a.on_turn("goofspiel")
    def boom(_v):
        raise ValueError("my strategy bug")

    status, body = a.decide_turn({"game": "goofspiel", "round": 1, "legal_actions": [1, 2]})
    assert status == 500
    assert body["error"] == "handler_error"
    assert "my strategy bug" in body["message"]  # the real error, not a generic string


def test_async_step_is_awaited():
    # `async def` handlers are first-class (async LLM clients); the SDK awaits them
    # instead of 500-ing on a returned coroutine.
    a = Agent(supported_games=["goofspiel"], name="t")

    @a.on_turn("goofspiel")
    async def decide(v):
        return {"round": v.round, "card": min(v.legal_actions)}

    status, body = a.decide_turn({"game": "goofspiel", "round": 2, "legal_actions": [3, 7]})
    assert status == 200
    assert body == {"round": 2, "card": 3}
