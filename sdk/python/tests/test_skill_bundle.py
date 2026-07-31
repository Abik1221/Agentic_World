"""The shipped skill must be present and its template must actually work.

A skill is what an AI coding assistant loads to learn this SDK, so a template that
does not run — or that quietly violates the platform contract — teaches every
assistant that reads it to write the same broken agent. It is worse than no template.
"""

import importlib.util
from pathlib import Path

SKILL = Path(__file__).resolve().parents[1] / "pyyol" / "skill"


def test_skill_ships_with_the_package():
    assert (SKILL / "SKILL.md").exists(), "SKILL.md is not bundled"
    for f in ("template_agent.py", "troubleshooting.md", "games.md"):
        assert (SKILL / "references" / f).exists(), f"reference {f} is not bundled"


def test_skill_frontmatter_is_valid():
    """name and description are REQUIRED — an assistant uses the description to decide
    whether the skill is relevant at all, so a missing one makes it undiscoverable."""
    text = (SKILL / "SKILL.md").read_text()
    assert text.startswith("---\n"), "SKILL.md must open with YAML frontmatter"
    fm = text.split("---", 2)[1]
    assert "name:" in fm and "description:" in fm
    # The description has to say WHEN to use it, not just what it is.
    assert "Use when" in fm or "use when" in fm


def _template():
    spec = importlib.util.spec_from_file_location(
        "_tpl", SKILL / "references" / "template_agent.py"
    )
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class _View:
    match_id = "m_1"
    round = 4
    prize_pool = 11
    legal_actions = [3, 6, 12]
    history = [{"round": i, "opp_card": i, "your_card": 14 - i} for i in range(1, 4)]


def test_template_returns_a_legal_move():
    a = _template().Atlas()
    mv = a.step(_View())
    assert mv.card in _View.legal_actions, "the shipped template plays an ILLEGAL move"
    assert mv.round == _View.round, "the template must echo the round"
    assert mv.rationale, "the template should model setting a rationale"


def test_template_keeps_matches_isolated():
    """The single most expensive trap on this platform: state built once and reused
    across matches, which looks exactly like a strategy bug."""
    a = _template().Atlas()
    a.step(_View())

    class Other(_View):
        match_id = "m_2"
        round = 1

    a.step(Other())
    assert set(a._memory) == {"m_1", "m_2"}, "template leaked state across matches"


def test_template_is_idempotent_per_round():
    a = _template().Atlas()
    first = a.step(_View())
    again = a.step(_View())  # a reconnect can redeliver the same turn
    assert again.card in _View.legal_actions
    assert first.round == again.round
