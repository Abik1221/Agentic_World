"""`pyyol doctor`'s verified-tier section.

The section exists because an agent can run perfectly and earn nothing: the platform ranks
VERIFIED play, so an agent whose calls are never proven is invisible to the model board however
well it plays. Nothing else in the toolchain says so, and learning it from an empty leaderboard
row weeks later is the failure this prevents.
"""

from __future__ import annotations

import contextlib
import io
from types import SimpleNamespace

from pyyol import _instrument, scaffold
from pyyol.cli import _print_verified_readiness, _scaffold_hint


def render(base="", creds=None, cfg=None) -> str:
    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):
        _print_verified_readiness(base, creds, cfg)
    return buf.getvalue()


def test_unrouted_agent_is_told_it_cannot_be_proven():
    # The most important line in the command. Without routing there is nothing to prove, so the
    # agent will never reach the model board — and every other check will look fine.
    _instrument.disable_gateway()
    out = render()
    assert "gateway routing" in out
    assert "pyyol.route" in out, "the fix must be named, not just the problem"
    assert "model board" in out


def test_routed_agent_is_told_calls_are_observed():
    _instrument.enable_gateway("sk_arena_x", "https://api.pyyol.test/v1")
    try:
        assert "server-observed" in render()
    finally:
        _instrument.disable_gateway()


def test_an_agent_with_no_system_prompt_is_told_why_it_is_excluded(tmp_path):
    # The scaffold rule, surfaced where a developer can act on it. Instructions living in the user
    # turn cannot be fingerprinted, so the agent is excluded from paired model comparison — and the
    # message has to say what to change, not merely that something is wrong.
    agent = tmp_path / "agent.py"
    agent.write_text(
        "def step(view):\n"
        "    return client.chat.completions.create(\n"
        "        model='gpt-4o',\n"
        "        messages=[{'role': 'user', 'content': 'play well ' + str(view)}])\n"
    )
    cfg = SimpleNamespace(entry=f"{agent}:agent")
    assert _scaffold_hint(cfg) is False
    out = render(cfg=cfg)
    assert "system prompt" in out
    assert "system message" in out, "the message must name the fix"


def test_an_agent_with_a_system_prompt_is_told_it_qualifies(tmp_path):
    agent = tmp_path / "agent.py"
    agent.write_text(
        "SYSTEM = 'You play Goofspiel.'\n"
        "def step(view):\n"
        "    return client.messages.create(system=SYSTEM, messages=[{'role': 'user', 'content': 'x'}])\n"
    )
    cfg = SimpleNamespace(entry=f"{agent}:agent")
    assert _scaffold_hint(cfg) is True
    assert "eligible" in render(cfg=cfg)


def test_an_unreadable_entry_says_unknown_rather_than_accusing(tmp_path):
    # "We could not tell" and "you did it wrong" are different claims. Guessing the second sends a
    # developer to fix something that is not broken.
    cfg = SimpleNamespace(entry=str(tmp_path / "missing.py") + ":agent")
    assert _scaffold_hint(cfg) is None
    out = render(cfg=cfg)
    assert "could not inspect" in out
    assert "trace" in out, "an unknown answer must point at the authoritative one"


def test_no_config_is_unknown_not_a_failure():
    assert _scaffold_hint(None) is None


def test_the_openai_developer_role_counts_as_a_system_prompt(tmp_path):
    # "developer" is OpenAI's newer name for the system role. Missing it would tell a correctly
    # built agent it is ineligible, which is worse than saying nothing.
    agent = tmp_path / "agent.py"
    agent.write_text("msgs = [{'role': 'developer', 'content': 'You play Mafia.'}]\n")
    assert _scaffold_hint(SimpleNamespace(entry=f"{agent}:agent")) is True


def test_the_scaffold_explanation_is_the_shared_one():
    # The doctor must not paraphrase the rule. The SDK, the trace and this command have to give a
    # developer the same sentence, or one of them drifts into being wrong.
    assert "system message" in scaffold.explain(scaffold.ISSUE_NO_SYSTEM_PROMPT)
