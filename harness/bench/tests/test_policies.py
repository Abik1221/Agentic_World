"""Tests for the parts that must never cost a turn.

These are the failure modes that would corrupt a paid run rather than merely annoy: an
illegal move the engine refuses, a fallback that quietly plays strategy, a budget that does
not actually stop, a system prompt that churns and takes the seat out of paired comparison.
Every one of them is cheap to test and expensive to discover on a live match.
"""

from __future__ import annotations

import json
from types import SimpleNamespace

import pytest

from pyyolbench.budget import Budget, BudgetExhausted
from pyyolbench.games.goofspiel import GoofspielPolicy
from pyyolbench.games.mafia import MafiaPolicy
from pyyolbench.games.monopoly import MonopolyPolicy
from pyyolbench.ledger import Ledger
from pyyolbench.models import RANKED, resolve


def goof_view(**kw):
    d = {
        "match_id": "m1",
        "seat": 0,
        "round": 3,
        "current_prize": 9,
        "prize_pool": 9,
        "your_hand": [2, 5, 7, 11],
        "legal_actions": [2, 5, 7, 11],
        "scores": [12, 8],
        "history": [
            {"round": 1, "prize": 4, "prize_pool": 4, "your_card": 3, "opp_card": 1, "winner": 0,
             "scores": [4, 0]},
            {"round": 2, "prize": 8, "prize_pool": 8, "your_card": 12, "opp_card": 13, "winner": 1,
             "scores": [4, 8]},
        ],
    }
    d.update(kw)
    ns = SimpleNamespace(**d)
    ns.raw = d
    return ns


# --- the scaffold invariant --------------------------------------------------------


@pytest.mark.parametrize("policy", [GoofspielPolicy(), MafiaPolicy(), MonopolyPolicy()])
def test_system_prompt_is_constant(policy):
    """The prompt must not vary between calls, or the fingerprint churns.

    An unstable fingerprint is not a cosmetic problem: the SDK reports the agent as
    ineligible for paired comparison, so the run produces matches that cost money and can
    never be attributed to a model.
    """
    assert policy.system_prompt() == policy.system_prompt()
    assert policy.system_prompt()


def _fingerprint_case(game: str):
    """Two genuinely different states for one game.

    A factory rather than a `parametrize` literal because the view builders are defined
    further down this file and `parametrize` arguments are evaluated at import time.
    """
    if game == "goofspiel":
        return GoofspielPolicy(), [goof_view(), goof_view(round=9, prize_pool=22, seat=1)]
    if game == "mafia":
        return MafiaPolicy(), [mafia_view(), mafia_view(day=5, phase="voting", legal=["vote"])]
    return MonopolyPolicy(), [
        mono_view(),
        mono_view(phase="acquire", legal_actions=["buy", "decline"]),
    ]


@pytest.mark.parametrize("game", ["goofspiel", "mafia", "monopoly"])
def test_scaffold_fingerprint_is_stable_across_turns(game):
    """The REAL invariant: the SDK's own fingerprint must not move between decisions.

    Testing the prompt string for state-shaped substrings was a bad proxy — Mafia's prompt
    legitimately contains the phrase "seat 0" because the rule that seat 0 is a real player
    is part of the rules. What actually matters is what the SDK hashes, so this asks the
    SDK directly, with two genuinely different game states, and requires the answer to be
    the same. That is exactly the condition under which two models can be paired.
    """
    from pyyol import scaffold_from_request

    policy, views = _fingerprint_case(game)
    fps = set()
    for v in views:
        request = {
            "model": "some/model",
            "messages": [
                {"role": "system", "content": policy.system_prompt()},
                {"role": "user", "content": policy.render_state(v, memory="notes here")},
            ],
            "tools": [{"type": "function", "function": {"name": "play_card"}}],
            "max_tokens": 2048,
        }
        fps.add(scaffold_from_request(request, endpoint="chat.completions"))
    assert len(fps) == 1, f"scaffold churned across turns: {fps}"
    assert next(iter(fps))


def test_scaffold_fingerprint_ignores_cache_control_markers():
    """Vendor cache markers must not split one harness into two scaffolds.

    The brain marks the system block cacheable for Anthropic and Google and leaves it a
    plain string for OpenAI and DeepSeek. If that shape difference reached the hash, the
    five models would land in two scaffold epochs and could never be compared — which is
    the entire experiment. `scaffold._text_of` reads only the `text` field, so it does
    not; this pins that.
    """
    from pyyol import scaffold_from_request

    text = GoofspielPolicy().system_prompt()
    base = {
        "model": "m",
        "tools": [{"type": "function", "function": {"name": "play_card"}}],
        "max_tokens": 2048,
    }
    plain = scaffold_from_request(
        {**base, "messages": [{"role": "system", "content": text},
                              {"role": "user", "content": "state"}]},
        endpoint="chat.completions",
    )
    cached = scaffold_from_request(
        {**base, "messages": [
            {"role": "system", "content": [
                {"type": "text", "text": text, "cache_control": {"type": "ephemeral"}}]},
            {"role": "user", "content": "state"},
        ]},
        endpoint="chat.completions",
    )
    assert plain == cached


def test_scaffold_fingerprint_ignores_the_model():
    """Swapping the model must NOT change the scaffold — that is what makes pairing work."""
    from pyyol import scaffold_from_request

    def fp(model: str) -> str:
        return scaffold_from_request(
            {
                "model": model,
                "messages": [
                    {"role": "system", "content": GoofspielPolicy().system_prompt()},
                    {"role": "user", "content": "state"},
                ],
                "tools": [{"type": "function", "function": {"name": "play_card"}}],
                "max_tokens": 2048,
            },
            endpoint="chat.completions",
        )

    assert fp("anthropic/claude-opus-5") == fp("deepseek/deepseek-v4-pro")


# --- Goofspiel ----------------------------------------------------------------------


def test_goofspiel_deduces_opponent_hand_exactly():
    """Opponent hand is the full deck minus what history shows them spending."""
    p = GoofspielPolicy()
    text = p.render_state(goof_view(), memory="")
    # They played 1 and 13; everything else is still in their hand.
    assert "[2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12]" in text
    assert "their highest is 12" in text


def test_goofspiel_renders_pool_not_just_prize():
    """A carried pool must be shown as the number at stake."""
    p = GoofspielPolicy()
    text = p.render_state(goof_view(current_prize=5, prize_pool=17), memory="")
    assert "Pool at stake THIS round: 17" in text
    assert "a tie has carried value in" in text


def test_goofspiel_coerce_accepts_legal_and_repairs_illegal():
    p = GoofspielPolicy()
    v = goof_view()
    move, src = p.coerce({"card": 7}, v)
    assert move["card"] == 7 and src == "model"
    # 13 is not held; nearest legal is 11 — the intent survives, the illegality does not.
    move, src = p.coerce({"card": 13}, v)
    assert move["card"] == 11 and src == "repair"
    move, src = p.coerce({"card": "not-a-number"}, v)
    assert move["card"] in v.legal_actions and src == "repair"


def test_goofspiel_fallback_is_inert():
    """The fallback must be the weakest legal move, never a strategy.

    If the fallback were good, a model that failed often would be scored on the
    fallback's play rather than its own, and failure would look like competence.
    """
    p = GoofspielPolicy()
    assert p.fallback(goof_view())["card"] == 2  # the lowest card held


def test_goofspiel_move_echoes_round():
    """`round` guards against acting on a stale view; it must always be present."""
    p = GoofspielPolicy()
    move, _ = p.coerce({"card": 5}, goof_view(round=7))
    assert move["round"] == 7


# --- Mafia --------------------------------------------------------------------------


def mafia_view(**kw):
    d = {
        "match_id": "m2",
        "your_seat": 3,
        "your_role": "Doctor",
        "day": 2,
        "phase": "night",
        "alive": {"0": True, "1": True, "2": False, "3": True, "4": True},
        "legal": ["protect"],
        "public": [],
        "private": [],
        "cannot_protect": 4,
    }
    d.update(kw)
    ns = SimpleNamespace(seat=d["your_seat"], phase=d["phase"])
    ns.raw = d
    return ns


def test_mafia_never_targets_seat_zero_as_nobody():
    """-1 means 'no seat'. 0 is a real player and must never stand in for nobody."""
    p = MafiaPolicy()
    v = mafia_view(your_role="Villager", legal=["vote"], phase="voting")
    move, _ = p.coerce({"kind": "vote", "target": -1}, v)
    # A vote must name a LIVING seat; -1 is repaired to a real one, not left as 0-by-accident.
    assert move["target"] in (0, 1, 4)
    assert move["action"] == "vote"


def test_mafia_doctor_cannot_repeat_protect():
    """`cannot_protect` is enforced locally so the engine never has to refuse the move."""
    p = MafiaPolicy()
    v = mafia_view()  # barred from protecting seat 4
    move, src = p.coerce({"kind": "protect", "target": 4}, v)
    assert move["target"] != 4 and src == "repair"
    # Self-protection is legal for the Doctor, unlike the targeting actions.
    move, src = p.coerce({"kind": "protect", "target": 3}, v)
    assert move["target"] == 3 and src == "model"


def test_mafia_dead_seats_are_never_targeted():
    p = MafiaPolicy()
    v = mafia_view(your_role="Mafia", legal=["night_kill"], allies=[0, 1])
    move, src = p.coerce({"kind": "night_kill", "target": 2}, v)  # seat 2 is dead
    assert move["target"] != 2 and src == "repair"


def test_mafia_renders_parity_count_for_mafia():
    """Parity is the decision-relevant number and the view does not supply it."""
    p = MafiaPolicy()
    text = p.render_state(mafia_view(your_role="Mafia", allies=[0, 1], legal=["night_kill"]), "")
    assert "2 Town gone to reach parity" in text or "reached parity" in text


def test_mafia_town_is_told_it_is_town():
    """Absence of `allies` is itself information; make it explicit rather than implicit."""
    p = MafiaPolicy()
    text = p.render_state(mafia_view(your_role="Detective", legal=["investigate"]), "")
    assert "you are TOWN" in text


def test_mafia_message_takes_text_from_prose():
    p = MafiaPolicy()
    v = mafia_view(phase="discussion", legal=["message"])
    move, src = p.coerce({"kind": "message", "target": -1, "_text": "Seat 4 dodged the question."}, v)
    assert move["action"] == "message"
    assert move["text"] == "Seat 4 dodged the question."
    assert src == "model"


# --- Monopoly -----------------------------------------------------------------------


def mono_view(**kw):
    d = {
        "match_id": "m3",
        "seat": 1,
        "phase": "manage",
        "legal_actions": ["build", "mortgage", "end_turn"],
        "state": {
            "players": [
                {"cash": 1200, "position": 5, "bankrupt": False},
                {"cash": 800, "position": 12, "bankrupt": False},
            ],
            "holdings": {"5": {"owner": 1, "houses": 2}, "12": {"owner": 0}},
        },
    }
    d.update(kw)
    ns = SimpleNamespace(seat=d["seat"], phase=d["phase"],
                         legal_actions=d["legal_actions"], state=d["state"])
    ns.raw = d
    return ns


def test_monopoly_rejects_action_outside_legal_list():
    """The legal list already encodes affordability and even-build; anything else is refused."""
    p = MonopolyPolicy()
    move, src = p.coerce({"kind": "buy"}, mono_view())  # not legal in `manage`
    assert move["action"] in mono_view().legal_actions and src == "repair"


def test_monopoly_fallback_prefers_ending_the_turn():
    p = MonopolyPolicy()
    assert p.fallback(mono_view())["action"] == "end_turn"
    # A roll-only phase has no decision in it; refusing to roll would wedge the match.
    assert p.fallback(mono_view(legal_actions=["roll"]))["action"] == "roll"


def test_monopoly_renders_only_own_holdings_in_full():
    """Sending all 40 squares every turn would break the cached prefix and re-bill the rules."""
    p = MonopolyPolicy()
    text = p.render_state(mono_view(), "")
    assert "Your properties: sq 5 2 houses" in text
    assert "seat 0: 1 squares" in text


def test_monopoly_passes_trade_payload_through():
    p = MonopolyPolicy()
    v = mono_view(legal_actions=["propose_trade", "end_turn"])
    trade = {"target": -1, "give_props": [5], "want_cash": 300}
    move, src = p.coerce({"kind": "propose_trade", "trade": trade}, v)
    assert move["trade"] == trade and src == "model"


# --- budget -------------------------------------------------------------------------


def test_budget_refuses_past_the_ceiling(tmp_path):
    b = Budget(tmp_path / "b.json", 1.00)
    b.reserve(0.60, model="x")
    b.settle(0.60, 0.60, model="x")
    b.reserve(0.30, model="x")
    with pytest.raises(BudgetExhausted):
        b.reserve(0.50, model="x")  # 0.60 settled + 0.30 reserved + 0.50 > 1.00


def test_budget_is_shared_across_instances(tmp_path):
    """Two processes must share one ceiling, not get one each.

    This is the failure that turns a $10 experiment into a $150 one when fifteen
    containers each believe they own the budget.
    """
    p = tmp_path / "b.json"
    a = Budget(p, 1.00)
    a.reserve(0.90, model="m")
    a.settle(0.90, 0.90, model="m")
    b = Budget(p, 1.00)  # a second process starting up
    assert b.snapshot().settled_usd == pytest.approx(0.90)
    with pytest.raises(BudgetExhausted):
        b.reserve(0.20, model="m")


def test_budget_releases_reservation_on_failure(tmp_path):
    """A failed call costs nothing but must not leak its reservation."""
    b = Budget(tmp_path / "b.json", 1.00)
    b.reserve(0.50, model="m")
    b.settle(0.50, 0.0, model="m")  # the 429 case
    assert b.snapshot().reserved_usd == pytest.approx(0.0)
    assert b.snapshot().settled_usd == pytest.approx(0.0)
    b.reserve(0.99, model="m")  # headroom is back


def test_budget_spend_contextmanager_settles_on_raise(tmp_path):
    b = Budget(tmp_path / "b.json", 1.00)
    with pytest.raises(ValueError):
        with b.spend(0.40, model="m") as box:
            box.append(0.01)
            raise ValueError("boom")
    assert b.snapshot().reserved_usd == pytest.approx(0.0)
    assert b.snapshot().settled_usd == pytest.approx(0.01)


# --- ledger -------------------------------------------------------------------------


def test_ledger_records_start_before_end(tmp_path):
    """A start with no end is how a lost call becomes visible."""
    led = Ledger(tmp_path / "l.jsonl")
    led.call_start(match_id="m", turn=1, model="x", game="goofspiel", seat=0)
    lines = [json.loads(x) for x in (tmp_path / "l.jsonl").read_text().splitlines()]
    assert lines[0]["kind"] == "call_start"
    assert lines[0]["match_id"] == "m"


def test_ledger_never_raises_on_bad_payload(tmp_path):
    """A ledger that could break a turn would be worse than no ledger."""
    led = Ledger(tmp_path / "l.jsonl")
    led.write("weird", obj=object())  # unserializable
    assert (tmp_path / "l.jsonl").read_text().strip() != ""


# --- pricing ------------------------------------------------------------------------


def test_cached_reads_are_not_double_counted():
    """`cached_read` is a subset of `prompt_tokens`, so it must be subtracted."""
    m = RANKED["opus-5"]
    full = m.cost_usd(1000, 0)
    cached = m.cost_usd(1000, 0, cached_read=1000)
    assert cached < full
    # 1000 tokens all cached at $0.50/M, not $5.00/M.
    assert cached == pytest.approx(1000 * 0.50 / 1e6)


def test_resolve_accepts_key_and_slug():
    assert resolve("opus-5").slug == "anthropic/claude-opus-5"
    assert resolve("anthropic/claude-opus-5").key == "opus-5"
    with pytest.raises(KeyError):
        resolve("gpt-9-imaginary")
