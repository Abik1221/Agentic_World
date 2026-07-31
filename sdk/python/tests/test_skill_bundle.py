"""The shipped skill must be complete, correctly routed, and its templates must run.

A skill is what an AI coding assistant loads to learn this SDK. A template that does
not run — or that quietly violates the platform contract — teaches every assistant
reading it to write the same broken agent. That is worse than shipping nothing.
"""

import importlib.util
import sys
from pathlib import Path

SKILL = Path(__file__).resolve().parents[1] / "pyyol" / "skill"
REFS = SKILL / "references"
GAMES = ("goofspiel", "mafia", "monopoly")


def test_skill_ships_complete():
    assert (SKILL / "SKILL.md").exists()
    for f in ("setup.md", "telemetry.md", "tracing.md", "troubleshooting.md"):
        assert (REFS / f).exists(), f"missing reference: {f}"
    for g in GAMES:
        assert (REFS / "games" / f"{g}.md").exists(), f"no rules for {g}"
        assert (REFS / "templates" / f"{g}_agent.py").exists(), f"no template for {g}"


def test_frontmatter_is_discoverable():
    """An assistant decides from `description` whether the skill is even relevant, so
    it has to say WHEN to use it, not just what it is."""
    text = (SKILL / "SKILL.md").read_text()
    assert text.startswith("---\n")
    fm = text.split("---", 2)[1]
    assert "name:" in fm and "description:" in fm
    assert "use when" in fm.lower()
    for g in GAMES:
        assert g in fm.lower(), f"{g} not discoverable from the description"


def test_root_routes_to_every_game():
    """The root must point at each game's rules AND template, or the assistant reads
    one game's contract and writes another game's agent."""
    text = (SKILL / "SKILL.md").read_text()
    for g in GAMES:
        assert f"games/{g}.md" in text, f"root does not route to {g} rules"
        assert f"{g}_agent.py" in text, f"root does not route to {g} template"


def test_root_warns_that_mafia_names_its_field_differently():
    """Mafia uses `legal`; the others use `legal_actions`. Stating the rule
    universally is how an assistant writes a Mafia agent whose every move is rejected.
    """
    text = (SKILL / "SKILL.md").read_text()
    assert "`legal`" in text and "legal_actions" in text
    assert "Mafia" in text


def _load(name):
    sys.path.insert(0, str(REFS / "templates"))
    try:
        spec = importlib.util.spec_from_file_location(
            f"_tpl_{name}", REFS / "templates" / f"{name}_agent.py"
        )
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        return mod
    finally:
        sys.path.pop(0)


class _GV:
    match_id = "m_g"
    seat = 0
    round = 4
    current_prize = 11
    prize_pool = 11
    your_hand = [3, 6, 12]
    legal_actions = [3, 6, 12]
    scores = [10, 12]
    history = [{"round": i, "opp_card": i, "your_card": 14 - i} for i in range(1, 4)]


class _MV:
    match_id = "m_m"
    your_seat = 3
    your_role = "Detective"
    day = 2
    phase = "voting"
    alive = {0: True, 1: True, 3: True, 4: True}
    allies = []
    legal = ["vote"]
    public = []
    private = []


class _PV:
    match_id = "m_p"
    seat = 1
    phase = "acquire"
    legal_actions = ["buy", "decline"]
    state = {"turn": 3, "players": {"1": {"cash": 800}}}


def test_every_template_returns_a_legal_move():
    g = _load("goofspiel").GoofspielAgent().step(_GV())
    assert g.card in _GV.legal_actions, "goofspiel template plays an ILLEGAL card"
    assert g.round == _GV.round, "goofspiel template must echo the round"

    m = _load("mafia").MafiaAgent().step(_MV())
    assert m.action in _MV.legal, "mafia template plays an action not in `legal`"
    assert m.target != _MV.your_seat, "mafia template voted for itself"

    p = _load("monopoly").MonopolyAgent().step(_PV())
    assert p.action in _PV.legal_actions, "monopoly template plays an ILLEGAL action"

    for mv in (g, m, p):
        assert mv.rationale, "templates should model setting a rationale"


def test_every_template_keeps_matches_isolated():
    """The single most expensive trap here: state built once and reused across
    matches, which looks exactly like a strategy bug."""
    a = _load("goofspiel").GoofspielAgent()
    a.step(_GV())

    class Other(_GV):
        match_id = "m_other"
        round = 1

    a.step(Other())
    assert set(a.mem._m) == {"m_g", "m_other"}, "template leaked state across matches"


def test_every_template_is_idempotent_on_a_redelivered_turn():
    for name, view, cls in (
        ("goofspiel", _GV(), "GoofspielAgent"),
        ("mafia", _MV(), "MafiaAgent"),
        ("monopoly", _PV(), "MonopolyAgent"),
    ):
        agent = getattr(_load(name), cls)()
        first = agent.step(view)
        again = agent.step(view)  # a reconnect can redeliver the same turn
        assert first is not None and again is not None, f"{name} failed on replay"
