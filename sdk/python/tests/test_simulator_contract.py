"""The simulator must agree with the platform it stands in for.

These are not simulator unit tests. They pin the three ways the harness silently
contradicted the real engine — each one punished a developer for following the
documentation, which is worse than having no harness at all.
"""

from typing import Any, Dict, List

from pyyol import Agent, simulate_goofspiel


def _recording_agent() -> tuple[Agent, List[Dict[str, Any]]]:
    """An agent that plays legally and keeps every turn view it was handed."""
    agent = Agent(secret="sim-secret")
    seen: List[Dict[str, Any]] = []

    @agent.on_turn("goofspiel")
    def _turn(v):
        seen.append(v.raw if hasattr(v, "raw") else dict(v.__dict__))
        return {"round": v.round, "card": min(v.legal_actions)}

    return agent, seen


def test_turn_view_carries_history():
    """`history` is documented as always present — an agent is told the view is
    self-contained. The harness used to omit it entirely, so a strategy written to
    the documented contract read an empty list and lost for no discoverable reason."""
    agent, seen = _recording_agent()
    simulate_goofspiel(agent, hand_size=5)

    assert seen, "agent was never given a turn"
    assert "history" in seen[0], "turn view must carry `history`"
    assert seen[0]["history"] == [], "round 1 has no history yet"

    last = seen[-1]["history"]
    assert len(last) == len(seen) - 1, f"history should hold every resolved round, got {len(last)}"

    # Documented field names — an agent must not need different code here and live.
    entry = last[0]
    for key in ("round", "prize", "prize_pool", "your_card", "opp_card", "winner", "scores"):
        assert key in entry, f"history entry missing documented field {key!r}: {entry}"


def test_rounds_are_one_based_like_the_engine():
    """The engine starts at round 1 and indexes prize_order[round-1]. The harness was
    0-based, so an agent tuned locally was off by one against the platform."""
    agent, seen = _recording_agent()
    simulate_goofspiel(agent, hand_size=5)

    rounds = [v["round"] for v in seen]
    assert rounds == [1, 2, 3, 4, 5], f"expected 1-based rounds, got {rounds}"


def test_seed_actually_changes_the_game():
    """`--seed` did nothing: prizes were never shuffled, so every run was the same
    in-order game and the RNG was dead code. A harness that cannot vary its scenario
    cannot tell you your agent is overfitted to one."""
    orders = []
    for seed in (1, 2, 3, 4, 5):
        agent, seen = _recording_agent()
        simulate_goofspiel(agent, hand_size=6, seed=seed)
        orders.append(tuple(v["current_prize"] for v in seen))

    assert len(set(orders)) > 1, f"seed had no effect — every run identical: {orders[0]}"

    # And the same seed must still reproduce exactly, or debugging is impossible.
    a1, s1 = _recording_agent()
    simulate_goofspiel(a1, hand_size=6, seed=42)
    a2, s2 = _recording_agent()
    simulate_goofspiel(a2, hand_size=6, seed=42)
    assert [v["current_prize"] for v in s1] == [v["current_prize"] for v in s2], (
        "same seed must reproduce"
    )


def test_adapter_receives_events():
    """An Adapter's on_event must actually fire.

    to_agent() used to wire the transport hook to `lambda _e: None`, so an Adapter
    received nothing regardless of what it defined — and in a game where reading the
    opponent IS the strategy, that removed the information silently.
    """
    from pyyol import Adapter

    got: List[Any] = []

    class Watcher(Adapter):
        name = "watcher"
        supported_games = ["goofspiel"]
        secret = "sim-secret"

        def step(self, view):
            return {"round": view.round, "card": min(view.legal_actions)}

        def on_event(self, event):
            got.append(event)

    simulate_goofspiel(Watcher().to_agent(), hand_size=5)
    assert got, "Adapter.on_event never fired — events are being discarded"
