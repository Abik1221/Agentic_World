"""A multi-module agent must load.

Loading by file path does not put the file's directory on sys.path, so any agent split
across more than one module failed with ModuleNotFoundError on its OWN package —
and `pyyol doctor` reported a bare "agent loads ✗" with no hint why. That limited
developers to single-file agents without ever saying so.
"""

import textwrap


def test_agent_can_import_a_sibling_module(tmp_path, monkeypatch):
    (tmp_path / "brains").mkdir()
    (tmp_path / "brains" / "__init__.py").write_text("STRATEGY = 'value-matching'\n")
    (tmp_path / "agent.py").write_text(
        textwrap.dedent(
            """
            from brains import STRATEGY
            from pyyol import Agent

            agent = Agent(secret="s")

            @agent.on_turn("goofspiel")
            def decide(v):
                return {"round": v.round, "card": min(v.legal_actions)}
            """
        )
    )

    from pyyol.cli import _load_agent

    loaded = _load_agent(str(tmp_path / "agent.py"), "agent")
    assert loaded is not None, "a two-module agent failed to load"


def test_agent_directory_goes_to_the_front_of_sys_path(tmp_path):
    """Front, not back: the agent's own modules must win over a same-named installed
    package, which is what a developer running from their project expects."""
    import sys

    from pyyol.cli import _add_agent_dir_to_syspath

    f = tmp_path / "agent.py"
    f.write_text("agent = None\n")
    _add_agent_dir_to_syspath(str(f))
    assert sys.path[0] == str(tmp_path)
