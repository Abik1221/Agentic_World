"""The link from a started match into the browser viewer.

Before this, `pyyol dev` printed a match id and nothing else — the developer had to go
find their own game in the UI. A wrong route here is worse than no link at all: it
lands them on a DIFFERENT live match and everything they watch is someone else's game.
"""

import argparse

from pyyol import cli


class FakeConsole:
    def __init__(self):
        self.lines = []

    def emit(self, kind, text):
        self.lines.append((kind, text))


def test_each_game_maps_to_its_real_viewer_route():
    d = "https://pyyol.com"
    assert cli._watch_url(d, "goofspiel", "m_1") == "https://pyyol.com/goofspiel?match=m_1"
    assert cli._watch_url(d, "monopoly", "mn_2") == "https://pyyol.com/monopoly?match=mn_2"
    # Mafia's viewer lives under /arena — /mafia only redirects there.
    assert cli._watch_url(d, "mafia", "mf_3") == "https://pyyol.com/arena/mafia?match=mf_3"


def test_match_ids_are_url_encoded():
    url = cli._watch_url("https://pyyol.com", "goofspiel", "m/1 2")
    assert " " not in url and "m/1" not in url.split("match=")[1]


def test_no_link_rather_than_a_wrong_one():
    """A link to 'some match' would be a lie dressed as a convenience."""
    assert cli._watch_url("https://pyyol.com", "goofspiel", "") == ""
    assert cli._watch_url("", "goofspiel", "m_1") == ""
    assert cli._watch_url("https://pyyol.com", "not-a-game", "m_1") == ""


def test_link_is_always_printed_even_when_the_browser_is_suppressed():
    """--open never must not also silence the link — the terminal is the fallback."""
    c = FakeConsole()
    args = argparse.Namespace(dashboard="https://pyyol.com", open_browser="never")
    cli._announce_match(c, args, "goofspiel", "m_9")
    joined = " ".join(t for _, t in c.lines)
    assert "started goofspiel match m_9" in joined
    assert "https://pyyol.com/goofspiel?match=m_9" in joined


def test_sandbox_matches_get_a_link_too():
    """Sandbox is where a developer spends most of their time; it is not second-class."""
    c = FakeConsole()
    args = argparse.Namespace(dashboard="https://pyyol.com", open_browser="never")
    cli._announce_match(c, args, "monopoly", "mn_sandbox_1", "1/3")
    joined = " ".join(t for _, t in c.lines)
    assert "mn_sandbox_1" in joined
    assert "/monopoly?match=mn_sandbox_1" in joined


def test_only_the_first_match_opens_a_tab(monkeypatch):
    """Sandbox iteration runs dozens of matches; a tab each is something you dread."""
    opened = []
    monkeypatch.setattr("webbrowser.open", lambda u: opened.append(u) or True)
    # Pretend we are on a real terminal, which the opener requires.
    monkeypatch.setattr(cli.sys.stdout, "isatty", lambda: True, raising=False)
    monkeypatch.setattr(cli.sys.stdin, "isatty", lambda: True, raising=False)
    cli._opened_once["done"] = False

    args = argparse.Namespace(dashboard="https://pyyol.com", open_browser="auto")
    for i in range(4):
        cli._announce_match(FakeConsole(), args, "goofspiel", f"m_{i}")
    assert len(opened) == 1, f"auto mode opened {len(opened)} tabs; want exactly 1"


def test_never_opens_in_a_non_tty(monkeypatch):
    """CI and remote shells: a browser that cannot open would spew over the match log."""
    opened = []
    monkeypatch.setattr("webbrowser.open", lambda u: opened.append(u) or True)
    monkeypatch.setattr(cli.sys.stdout, "isatty", lambda: False, raising=False)
    cli._opened_once["done"] = False

    args = argparse.Namespace(dashboard="https://pyyol.com", open_browser="always")
    cli._announce_match(FakeConsole(), args, "goofspiel", "m_ci")
    assert opened == []
