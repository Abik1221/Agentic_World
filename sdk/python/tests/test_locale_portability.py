"""The quickstart must work on a machine that is not configured in UTF-8.

Reported from a fresh Windows install: `pyyol init` and `pyyol run` both died on the
very first try. Investigating it turned up two independent faults, and neither is
actually Windows-specific — both reproduce on Linux, which is why they are tested here
rather than being written off as a Windows quirk.

1. ENCODING. Python's `open()` with no `encoding=` uses the locale's preferred encoding.
   The starter templates contain an em dash, so wherever that encoding is not UTF-8 the
   scaffold write raised UnicodeEncodeError. Reproduced on Linux under `LC_ALL=C` with
   coercion disabled, and under a latin-1 locale. The same applies in reverse when
   READING: `pyyol logs` decoded an agent's own log in the locale encoding and crashed
   on the first accented character.

   Note the escape hatch that hides this: PEP 538/540 mean a bare `LC_ALL=C` is normally
   coerced to UTF-8, which is why it does not fail on a default Fedora or Debian box.
   These tests disable that coercion ON PURPOSE, so they exercise the encoding a
   non-UTF-8 machine really has instead of the one CI happens to be set to.

2. ADAPTER. `pyyol init` scaffolds an Adapter instance, and an Adapter has no `.run()`.
   `pyyol dev`/`pyyol play` normalized through `as_agent`; `pyyol run` did a raw getattr
   and handed the Adapter straight to `.run()`. So the scaffold our own quickstart writes
   could not be run by the command the quickstart tells you to run.

Credit: both faults were found and first fixed by @Yeabsirashimelis in #56.
"""

import json
import os
import subprocess
import sys
from types import SimpleNamespace

import pytest

# A locale with no UTF-8 anywhere: C, with PEP 538 coercion and PEP 540 UTF-8 mode both
# switched off. Without these two the interpreter quietly upgrades us to UTF-8 and the
# test passes even against the unfixed code.
NON_UTF8_ENV = {
    "LC_ALL": "C",
    "LANG": "C",
    "PYTHONCOERCECLOCALE": "0",
    "PYTHONUTF8": "0",
}


def _run(code, cwd, extra_env=None):
    """Run `code` in a child interpreter that has no UTF-8 anywhere.

    Written to a FILE rather than passed with `-c`: under LC_ALL=C the command line
    itself is decoded in the locale encoding, so a non-ASCII `-c` string would fail on
    argv before reaching the code under test — a pass or fail that says nothing about
    the bug. Source files are always read as UTF-8 (PEP 3120), so a script isolates the
    thing being measured.
    """
    script = cwd / "_probe.py"
    script.write_text(code, encoding="utf-8")
    env = {**os.environ, **NON_UTF8_ENV, **(extra_env or {})}
    return subprocess.run(
        [sys.executable, str(script)],
        cwd=cwd,
        capture_output=True,
        text=True,
        env=env,
    )


def test_init_scaffolds_under_a_non_utf8_locale(tmp_path):
    """`pyyol init` is the first command anybody runs. It must not depend on the locale."""
    r = _run("from pyyol.cli import main; raise SystemExit(main(['init', 'a1']))", tmp_path)
    assert r.returncode == 0, (
        f"init failed under a non-UTF-8 locale (exit {r.returncode}).\n{r.stdout}\n{r.stderr}"
    )
    assert "UnicodeEncodeError" not in r.stderr

    d = tmp_path / "a1"
    # Read back as UTF-8 explicitly: the point is that the file is UTF-8 on disk no
    # matter what the writing machine's locale was.
    code = (d / "agent.py").read_text(encoding="utf-8")
    assert "—" in code, "the em dash must survive the write, not be mangled or dropped"
    assert json.loads((d / "manifest.json").read_text(encoding="utf-8"))["games"]


def test_config_round_trips_non_ascii_under_a_non_utf8_locale(tmp_path):
    """pyyol.toml holds a developer-supplied agent name, which can be any text at all."""
    r = _run(
        "from pyyol import config as c\n"
        "NAME = 'Ünicode Agent — ok'\n"
        "c.save(c.Config(name=NAME, entry='agent.py:agent'))\n"
        "back = c.load()\n"
        "assert back.name == NAME, repr(back.name)\n",
        tmp_path,
    )
    assert r.returncode == 0, f"pyyol.toml did not round-trip:\n{r.stdout}\n{r.stderr}"


def test_logs_reads_an_agent_log_under_a_non_utf8_locale(tmp_path):
    """`pyyol logs` is what a developer runs WHEN SOMETHING IS ALREADY WRONG.

    It is the last command that should ever crash, and it reads text produced by a
    model — so it must assume arbitrary characters rather than ASCII.
    """
    log = tmp_path / "agent.log"
    log.write_text("round 1 — José played 5\n", encoding="utf-8")
    r = _run(
        f"from pyyol.cli import main; raise SystemExit(main(['logs', '--file', {str(log)!r}]))",
        tmp_path,
    )
    assert r.returncode == 0, f"logs crashed (exit {r.returncode}):\n{r.stdout}\n{r.stderr}"
    assert "UnicodeDecodeError" not in r.stderr


def test_logs_survives_bytes_that_are_not_valid_utf8(tmp_path):
    """A killed process leaves a half-written line. Mojibake beats a traceback."""
    log = tmp_path / "agent.log"
    log.write_bytes(b"ok\n\xff\xfe truncated mid-char\n")
    r = _run(
        f"from pyyol.cli import main; raise SystemExit(main(['logs', '--file', {str(log)!r}]))",
        tmp_path,
    )
    assert r.returncode == 0, f"logs crashed on undecodable bytes:\n{r.stdout}\n{r.stderr}"


def test_credentials_survive_a_corrupt_file(tmp_path, monkeypatch):
    """An unreadable credentials file means "not logged in", never a traceback.

    UnicodeDecodeError is a ValueError but NOT a JSONDecodeError, so it used to slip
    past both arms of the except and crash the CLI before it did anything.
    """
    from pyyol import credentials

    monkeypatch.setattr(credentials, "config_dir", lambda: str(tmp_path))
    p = tmp_path / credentials._cred_path().split(os.sep)[-1]
    p.write_bytes(b'{"url": "\xff\xfe not utf-8"}')
    assert credentials.load() is None


# --- the Adapter fault -----------------------------------------------------------


SCAFFOLD_STYLE_AGENT = """\
from pyyol import Adapter


class MyAgent(Adapter):
    name = "my-agent"

    def step(self, view):
        return {"action": "noop"}


# Exactly what `pyyol init` writes: an Adapter INSTANCE, which has no .run().
agent = MyAgent()
"""


def test_the_scaffolded_export_really_has_no_run():
    """Pins the premise. If Adapter ever grows a .run(), the guard below proves nothing."""
    from pyyol import Adapter

    assert not hasattr(Adapter, "run"), (
        "Adapter gained a .run() — re-check whether cmd_run still needs to normalize"
    )


def test_run_normalizes_an_adapter_before_calling_run(tmp_path, monkeypatch):
    """`pyyol run` on the scaffold must not raise AttributeError.

    Driven through cmd_run itself rather than through as_agent directly: the bug was
    never in as_agent, it was that this one load path never called it.
    """
    from pyyol import cli, credentials
    from pyyol.server import Agent

    (tmp_path / "agent.py").write_text(SCAFFOLD_STYLE_AGENT, encoding="utf-8")

    called = {}

    def fake_run(self, **kw):
        called["ok"] = True

    monkeypatch.setattr(Agent, "run", fake_run, raising=False)
    monkeypatch.setattr(credentials, "load", lambda: None)
    monkeypatch.setattr(cli, "_connection_token", lambda a, c: ("test-token", True))
    monkeypatch.setattr(cli, "_log_file_handler", lambda: None)

    args = SimpleNamespace(
        url="ws://127.0.0.1:9",
        agent="ag_test",
        file=str(tmp_path / "agent.py"),
        var="agent",
        json=False,
        quiet=True,
        no_color=True,
    )
    rc = cli.cmd_run(args)

    assert rc == 0, "cmd_run should have run the normalized agent"
    assert called.get("ok"), (
        "Agent.run was never reached — cmd_run handed the raw Adapter through, "
        "which is the AttributeError developers hit on the scaffold"
    )


def test_run_still_explains_a_genuinely_wrong_export(tmp_path, monkeypatch):
    """Normalizing must not swallow the friendly message for a bad `entry`."""
    from pyyol import cli, credentials

    (tmp_path / "agent.py").write_text("agent = 42\n", encoding="utf-8")
    monkeypatch.setattr(credentials, "load", lambda: None)
    monkeypatch.setattr(cli, "_connection_token", lambda a, c: ("test-token", True))
    monkeypatch.setattr(cli, "_log_file_handler", lambda: None)

    args = SimpleNamespace(
        url="ws://127.0.0.1:9",
        agent="ag_test",
        file=str(tmp_path / "agent.py"),
        var="agent",
        json=False,
        quiet=True,
        no_color=True,
    )
    rc = cli.cmd_run(args)
    assert rc == 2, "a non-Agent export should be a usage error, not a crash"


@pytest.mark.parametrize(
    "mod", ["pyyol.cli", "pyyol.config", "pyyol.credentials", "pyyol.install_ping"]
)
def test_no_bare_open_remains(mod):
    """A guard against the fix eroding: every text open() in the SDK names its encoding.

    Cheap to satisfy and easy to forget, and the failure mode is invisible on the
    UTF-8 machine of whoever writes the code — which is exactly how it shipped.
    """
    import importlib
    import inspect
    import re

    src = inspect.getsource(importlib.import_module(mod))
    bare = [
        ln.strip()
        for ln in src.splitlines()
        # Comments are skipped, or this very rule's own explanation counts as a violation.
        if not ln.lstrip().startswith("#")
        and re.search(r"(?<!os\.)\bopen\(", ln)
        and "encoding=" not in ln
        and not re.search(r'"[rw]b"|\'[rw]b\'|urlopen|webbrowser', ln)
    ]
    assert not bare, f"text open() without encoding= in {mod}: {bare}"


def test_serve_normalizes_an_adapter_before_calling_run(tmp_path, monkeypatch):
    """`pyyol serve` on the scaffold must not raise AttributeError.

    The SAME defect as cmd_run above, in the command that plays RANKED. cmd_run was
    fixed and its own comment recorded that "every other load path already normalized";
    serve was the one it missed, so the crash landed at the END of a developer's setup —
    after login, certification, funding and queueing — with an internal-error banner
    reading `'MinimalAgent' object has no attribute 'run'`, which looks like the
    developer's mistake on an agent our own quickstart scaffolded.
    """
    from pyyol import cli, credentials
    from pyyol.server import Agent

    (tmp_path / "agent.py").write_text(SCAFFOLD_STYLE_AGENT, encoding="utf-8")

    called = {}

    def fake_run(self, **kw):
        called["ok"] = True

    monkeypatch.setattr(Agent, "run", fake_run, raising=False)
    monkeypatch.setattr(credentials, "load", lambda: None)
    monkeypatch.setattr(cli, "_connection_token", lambda a, c: ("test-token", True))
    monkeypatch.setattr(cli, "_log_file_handler", lambda: None)
    # The autoplay call is a real HTTP request; the connection is held either way, so
    # its outcome must not decide whether the agent is normalized.
    monkeypatch.setattr(cli, "_autoplay_set", lambda *a, **k: (0, {"error": "offline"}))

    args = SimpleNamespace(
        url="ws://127.0.0.1:9",
        api="http://127.0.0.1:9",
        agent="ag_test",
        token="",
        file=str(tmp_path / "agent.py"),
        var="agent",
        ranked=True,
        mode="",
        bid=500,
        games="goofspiel",
        json=False,
        quiet=True,
        no_color=True,
    )
    rc = cli.cmd_serve(args)

    assert rc == 0, "cmd_serve should have run the normalized agent"
    assert called.get("ok"), (
        "Agent.run was never reached — cmd_serve handed the raw Adapter through, which "
        "is the AttributeError developers hit when trying to play ranked"
    )
