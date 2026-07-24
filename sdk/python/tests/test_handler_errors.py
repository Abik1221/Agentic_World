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
