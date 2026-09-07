"""The CLI must never show a developer our tracebacks.

`main()` ended in a bare `return args.func(args)`, so any unexpected exception printed OUR file
paths and line numbers to somebody who wanted to know whether their agent was ranked — and
Ctrl-C printed a stack trace as if stopping a program were a crash. These pin the replacement.
"""

from __future__ import annotations

import pytest

from pyyol import _crash, runtime


def test_an_internal_fault_exits_70_and_says_whose_bug_it_is(capsys, tmp_path, monkeypatch):
    monkeypatch.setenv("XDG_STATE_HOME", str(tmp_path))
    monkeypatch.delenv("PYYOL_DEBUG", raising=False)

    def boom(_args):
        raise ValueError("internal detail nobody asked about")

    code = _crash.guard(boom, None, version="9.9.9", argv=["whoami"])

    assert code == _crash.EXIT_INTERNAL, (
        "an internal fault must be distinguishable from a reported failure"
    )
    err = capsys.readouterr().err
    # The single most important line: a developer who thinks they broke it goes hunting
    # through their own agent code for a fault that is ours.
    assert "bug in pyyol, not in your agent" in err
    assert "ValueError: internal detail nobody asked about" in err
    assert "Traceback (most recent call last)" not in err, (
        "the raw traceback must not be the default output"
    )
    assert "PYYOL_DEBUG=1" in err, "the developer must be told how to get the full detail"
    # The report exists and carries what we need to fix it.
    report = tmp_path / "pyyol" / "last-crash.log"
    assert report.exists()
    body = report.read_text()
    assert "Traceback" in body and "9.9.9" in body


def test_the_crash_report_records_the_command_but_not_its_arguments(tmp_path, monkeypatch):
    """A crash file is something a developer may paste into a public issue, and pyyol's own
    arguments include agent names and, on some commands, tokens."""
    monkeypatch.setenv("XDG_STATE_HOME", str(tmp_path))

    def boom(_args):
        raise RuntimeError("x")

    _crash.guard(boom, None, version="1", argv=["publish", "--token", "secret-token-value"])
    body = (tmp_path / "pyyol" / "last-crash.log").read_text()
    assert "publish" in body
    assert "secret-token-value" not in body, "a crash report must never carry argument values"


def test_ctrl_c_is_quiet_and_exits_130(capsys, monkeypatch):
    monkeypatch.delenv("PYYOL_DEBUG", raising=False)

    def interrupted(_args):
        raise KeyboardInterrupt

    code = _crash.guard(interrupted, None, version="1", argv=["dev"])
    assert code == _crash.EXIT_INTERRUPTED, "128 + SIGINT, so `&&` chains stop when a human does"
    err = capsys.readouterr().err
    assert "Stopped." in err
    assert "KeyboardInterrupt" not in err, (
        "printing a stack trace to explain the user's own keystroke is noise"
    )


def test_a_deliberate_exit_code_is_not_swallowed():
    """A command that has already decided its exit code has made a decision. Overriding a
    deliberate sys.exit(2) with a crash report about nothing would be worse than the bug."""

    def deliberate(_args):
        raise SystemExit(2)

    with pytest.raises(SystemExit) as got:
        _crash.guard(deliberate, None, version="1", argv=["init"])
    assert got.value.code == 2


def test_a_successful_command_passes_its_code_through():
    assert _crash.guard(lambda _a: 0, None, version="1", argv=["whoami"]) == 0
    assert _crash.guard(lambda _a: 1, None, version="1", argv=["whoami"]) == 1


def test_a_crash_survives_an_unwritable_report_directory(capsys, monkeypatch, tmp_path):
    """Failing to write the report must never replace the crash message with a different error."""
    monkeypatch.setattr(_crash, "_crash_dir", lambda: (_ for _ in ()).throw(OSError("read-only")))

    def boom(_args):
        raise ValueError("still needs reporting")

    # The thrown OSError is raised inside _write_report's own try, so guard must still return.
    code = _crash.guard(boom, None, version="1", argv=["whoami"])
    assert code == _crash.EXIT_INTERNAL
    assert "bug in pyyol" in capsys.readouterr().err


# ── expected failures, not crashes ───────────────────────────────────────────
#
# The crash boundary above covers faults nobody planned for. These cover the errors a
# developer is SUPPOSED to hit — where the whole value is whether the message tells them what
# to do next.


def test_a_status_of_zero_is_not_shown_as_one():
    """The HTTP helpers return 0 for "no response at all" (offline, refused, DNS).

    Printing it gave "could not fetch leaderboard (0)", where the only number on the line is
    fake and a reader's first instinct is to look up status zero. The cause is already in the
    text that follows.
    """
    from pyyol import cli

    assert cli._status(0) == "", "a non-status must print nothing at all"
    assert cli._status(404) == " (404)", "a real status is worth showing"
    assert cli._status(500) == " (500)"


def test_profile_without_a_handle_says_to_log_in(capsys, monkeypatch, tmp_path):
    """`pyyol profile` is documented as "self if omitted".

    Logged out there is no self, and the old message was "pass a handle" — true, and it hides
    the actual fix. A developer reads it as "this command needs an argument" and never learns
    that logging in is what they wanted.
    """
    import argparse

    from pyyol import cli, credentials

    monkeypatch.setattr(credentials, "load", lambda: None)
    code = cli.cmd_profile(argparse.Namespace(handle=None, api="https://example.invalid"))

    assert code == 2
    err = capsys.readouterr().err
    assert "not logged in" in err, "the real cause must be named"
    assert "pyyol login" in err, "and the fix must be one copyable command"
    # The other route stays offered: someone may simply want to look at another developer.
    assert "pyyol profile <@handle>" in err


def test_api_is_accepted_before_the_command_as_well_as_after():
    """`pyyol --api URL leaderboard` is the position every other tool accepts.

    It used to die with an argparse error listing all 25 commands and claiming the URL was an
    invalid choice of command — a message that named the wrong problem entirely. The trap is
    that the top level and the subcommand parse into the SAME namespace, so an ordinary default
    on the subcommand's flag silently overwrites a global value with "".
    """
    from pyyol import cli

    parser = cli.build_parser()
    before = parser.parse_args(["--api", "https://before.example", "leaderboard"])
    after = parser.parse_args(["leaderboard", "--api", "https://after.example"])
    neither = parser.parse_args(["leaderboard"])

    assert before.api == "https://before.example", "a global --api must survive the subcommand"
    assert after.api == "https://after.example", "the per-command position must still work"
    assert neither.api == "", "absent means absent, so the usual resolution order still applies"


# ── a legacy console must not turn output into a crash ───────────────────────


def test_output_is_made_unicode_safe_before_anything_prints():
    """Windows consoles still default to cp1252 in plenty of setups, and this CLI prints →, ●,
    box-drawing and a block-glyph wordmark. Writing any of those to a cp1252 stream raises
    UnicodeEncodeError from inside `print`, so `pyyol --help` died with a traceback before
    printing one line of help — on the platform least equipped to debug it.

    Reproduced with PYTHONIOENCODING=cp1252 before the fix; it is a pre-existing bug, not a new
    one: the arrows in the parser description were enough on their own.
    """
    from pyyol import cli

    class FakeStream:
        def __init__(self, encoding):
            self.encoding = encoding
            self.calls = []

        def reconfigure(self, **kw):
            self.calls.append(kw)
            if "encoding" in kw:
                self.encoding = kw["encoding"]

    legacy = FakeStream("cp1252")
    already = FakeStream("utf-8")

    import sys as _sys

    orig_out, orig_err = _sys.stdout, _sys.stderr
    try:
        _sys.stdout, _sys.stderr = legacy, already
        cli._make_output_unicode_safe()
    finally:
        _sys.stdout, _sys.stderr = orig_out, orig_err

    assert legacy.calls, "a cp1252 stream must be reconfigured"
    assert legacy.calls[0].get("encoding") == "utf-8", (
        "prefer switching to UTF-8: modern Windows Terminal renders it, so the right answer is "
        "usually to use it rather than to degrade the output"
    )
    assert not already.calls, "a stream that is already UTF-8 must be left alone"


def test_a_stream_that_cannot_be_reconfigured_is_left_alone():
    """An embedded runtime or a test double may not implement reconfigure. Decoration must
    never be the reason a command cannot run."""
    from pyyol import cli

    class Bare:
        encoding = "cp1252"  # no reconfigure attribute at all

    import sys as _sys

    orig_out, orig_err = _sys.stdout, _sys.stderr
    try:
        _sys.stdout, _sys.stderr = Bare(), Bare()
        cli._make_output_unicode_safe()  # must not raise
    finally:
        _sys.stdout, _sys.stderr = orig_out, orig_err


def test_the_wordmark_is_dropped_when_the_stream_cannot_draw_it():
    """A logo that can break `--help` is not a logo, it is an outage with a brand on it."""
    from pyyol import shell

    class Stream:
        def __init__(self, encoding):
            self.encoding = encoding

        def isatty(self):
            return False

    assert shell.wordmark_for(Stream("utf-8")), "a UTF-8 stream should get the wordmark"
    assert shell.wordmark_for(Stream("ascii")) == "", "an ASCII stream must get nothing at all"


def test_a_revoked_credential_is_an_error_not_a_crash(capsys, monkeypatch, tmp_path):
    """A terminal connector failure must not wear the "report a bug" banner.

    ConnectorError is a decision the SERVER made about this credential — "this agent key
    was revoked, run `pyyol login`" — and its message already tells the developer exactly
    what to do. Routing it through the crash report buried that message under "pyyol hit
    an internal error / This is a bug in pyyol, not in your agent / Report it", which asks
    someone to file a bug for a ten-second fix and to distrust a tool behaving correctly.

    Hit for real mid-setup, where it read as yet another platform failure.
    """
    monkeypatch.delenv("PYYOL_DEBUG", raising=False)
    monkeypatch.setenv("PYYOL_STATE_HOME", str(tmp_path))

    def revoked(_args):
        raise runtime.ConnectorError("this agent key was revoked — run `pyyol login`")

    code = _crash.guard(revoked, None, version="1", argv=["serve"])
    err = capsys.readouterr().err

    assert "this agent key was revoked" in err, "the actionable message must survive"
    assert "internal error" not in err, "a revoked credential is not an internal fault"
    assert "bug in pyyol" not in err, "this asks the developer to report a working tool"
    assert code == 1, (
        "exit 1 — the command ran and told you it failed. EXIT_INTERNAL means the command "
        "itself broke, and CI should be able to tell those apart"
    )
    assert code != _crash.EXIT_INTERNAL


def test_a_genuine_fault_still_reports_as_a_bug(capsys, monkeypatch, tmp_path):
    """The other half: narrowing the boundary must not swallow real crashes."""
    monkeypatch.delenv("PYYOL_DEBUG", raising=False)
    monkeypatch.setenv("PYYOL_STATE_HOME", str(tmp_path))

    def broken(_args):
        raise AttributeError("'MinimalAgent' object has no attribute 'run'")

    code = _crash.guard(broken, None, version="1", argv=["serve"])
    err = capsys.readouterr().err

    assert code == _crash.EXIT_INTERNAL
    assert "internal error" in err and "bug in pyyol" in err
