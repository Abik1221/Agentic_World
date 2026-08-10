"""The interactive shell: the front door, and the rules that keep it from being a trap."""

from __future__ import annotations

import argparse
import io

import pytest

from pyyol import cli, shell


def _parser_factory(calls: list[str]):
    """A tiny stand-in for build_parser() with the same shape argparse produces."""

    def build():
        p = argparse.ArgumentParser(prog="pyyol")
        sub = p.add_subparsers(dest="command", required=True, metavar="<command>")
        ok = sub.add_parser("games", help="show live + waiting agents per game")
        ok.add_argument("--api", default="")
        ok.set_defaults(func=lambda a: calls.append("games") or 0)
        boom = sub.add_parser("explode", help="raises")
        boom.set_defaults(func=lambda a: (_ for _ in ()).throw(RuntimeError("kaboom")))
        stop = sub.add_parser("longrun", help="ctrl-c")
        stop.set_defaults(func=lambda a: (_ for _ in ()).throw(KeyboardInterrupt()))
        return p

    return build


def _run(monkeypatch, lines: list[str], build) -> str:
    """Drive the shell with a scripted stdin and return everything it printed."""
    out = io.StringIO()
    out.isatty = lambda: False  # type: ignore[method-assign]  # deterministic: no ANSI in assertions
    it = iter(lines)

    def fake_input(_prompt: str = "") -> str:
        try:
            return next(it)
        except StopIteration:
            raise EOFError  # end of script = Ctrl-D

    monkeypatch.setattr("builtins.input", fake_input)
    monkeypatch.setattr(shell, "_install_readline", lambda cmds: None)
    monkeypatch.setattr(shell, "_whoami", lambda api: None)
    shell.run_shell(build, "9.9.9", "https://api.test", stream=out)
    return out.getvalue()


# ── THE HARD RULE ───────────────────────────────────────────────────────────────
#
# A prompt that waits on stdin in CI, a cron entry, a Dockerfile RUN or `pyyol | cat`
# hangs the pipeline forever — in exactly the places nobody is watching it. So the shell
# is only ever entered on a TTY, and everything else prints help and exits.


def test_no_tty_prints_help_and_never_opens_the_shell(monkeypatch, capsys):
    entered = []
    monkeypatch.setattr(shell, "run_shell", lambda *a, **k: entered.append(1) or 0)
    monkeypatch.setattr("sys.argv", ["pyyol"])
    monkeypatch.setattr("sys.stdin.isatty", lambda: False)
    monkeypatch.setattr("sys.stdout.isatty", lambda: True)

    rc = cli.main()

    assert rc == 0
    assert entered == [], "a non-TTY stdin opened an interactive prompt — this hangs CI"
    assert "usage: pyyol" in capsys.readouterr().out


def test_a_tty_opens_the_shell(monkeypatch):
    entered = []
    monkeypatch.setattr(shell, "run_shell", lambda *a, **k: entered.append(1) or 0)
    monkeypatch.setattr("sys.argv", ["pyyol"])
    monkeypatch.setattr("sys.stdin.isatty", lambda: True)
    monkeypatch.setattr("sys.stdout.isatty", lambda: True)

    assert cli.main() == 0
    assert entered == [1]


def test_an_explicit_command_never_opens_the_shell(monkeypatch):
    # `pyyol whoami` must behave exactly as it always has, TTY or not.
    entered = []
    monkeypatch.setattr(shell, "run_shell", lambda *a, **k: entered.append(1) or 0)
    monkeypatch.setattr("sys.stdin.isatty", lambda: True)
    monkeypatch.setattr("sys.stdout.isatty", lambda: True)
    with pytest.raises(SystemExit):
        cli.main(["--version"])
    assert entered == []


# ── THE SESSION MUST OUTLIVE ANY SINGLE COMMAND ─────────────────────────────────


def test_help_lists_the_real_commands(monkeypatch):
    calls: list[str] = []
    text = _run(monkeypatch, ["/help", "/exit"], _parser_factory(calls))
    # Read off the parser, never a hand-kept list, so it cannot list a command that was
    # removed or miss one that was just added.
    assert "/games" in text
    assert "show live + waiting agents per game" in text


def test_a_command_runs_and_arguments_are_passed_through(monkeypatch):
    calls: list[str] = []
    _run(monkeypatch, ["/games --api https://x", "/exit"], _parser_factory(calls))
    assert calls == ["games"]


def test_a_leading_slash_is_optional(monkeypatch):
    calls: list[str] = []
    _run(monkeypatch, ["games", "/exit"], _parser_factory(calls))
    assert calls == ["games"]


def test_an_unknown_command_does_not_end_the_session(monkeypatch):
    calls: list[str] = []
    text = _run(monkeypatch, ["/nope", "/games", "/exit"], _parser_factory(calls))
    assert "unknown command" in text
    assert calls == ["games"], "the session died on a typo"


def test_a_bad_flag_does_not_end_the_session(monkeypatch):
    # argparse calls exit() on an unrecognised flag. In a shell that must return to the
    # prompt, not kill the process.
    calls: list[str] = []
    _run(monkeypatch, ["/games --not-a-flag", "/games", "/exit"], _parser_factory(calls))
    assert calls == ["games"]


def test_a_raising_command_does_not_end_the_session(monkeypatch):
    calls: list[str] = []
    text = _run(monkeypatch, ["/explode", "/games", "/exit"], _parser_factory(calls))
    assert "RuntimeError" in text and "kaboom" in text
    assert calls == ["games"]


def test_ctrl_c_stops_the_command_and_returns_to_the_prompt(monkeypatch):
    # Inside a shell, Ctrl-C means "stop this match/run", not "quit the tool" — leaving is
    # /exit. A long-running command (dev/play/watch) is the normal case for this.
    calls: list[str] = []
    text = _run(monkeypatch, ["/longrun", "/games", "/exit"], _parser_factory(calls))
    assert "stopped" in text
    assert calls == ["games"]


def test_an_unbalanced_quote_is_reported_not_fatal(monkeypatch):
    calls: list[str] = []
    text = _run(monkeypatch, ['/games --api "oops', "/games", "/exit"], _parser_factory(calls))
    assert "could not read that line" in text
    assert calls == ["games"]


def test_ctrl_d_at_the_prompt_leaves(monkeypatch):
    calls: list[str] = []
    # No /exit — the scripted input simply runs out, which raises EOFError like Ctrl-D.
    _run(monkeypatch, ["/games"], _parser_factory(calls))
    assert calls == ["games"]


def test_colour_is_off_without_a_tty(monkeypatch):
    calls: list[str] = []
    text = _run(monkeypatch, ["/help", "/exit"], _parser_factory(calls))
    assert "\x1b[" not in text, "ANSI escapes reached a non-tty stream"


def test_no_color_env_is_honoured(monkeypatch):
    monkeypatch.setenv("NO_COLOR", "1")
    stream = io.StringIO()
    stream.isatty = lambda: True  # type: ignore[method-assign]
    assert shell.use_color(stream) is False


# ── The palette is an ORDER, not a hand-kept list ────────────────────────────────


def _real_shell(monkeypatch, lines: list[str]) -> str:
    """Drive the shell with the REAL parser, so the palette is checked against the real
    twenty-five commands rather than a stub."""
    return _run(monkeypatch, lines, cli.build_parser)


def test_a_lone_slash_opens_the_palette(monkeypatch):
    # The banner advertises "type / for commands" — this is the thing it advertises, and it
    # is what a developer coming from another agent CLI reaches for first.
    text = _real_shell(monkeypatch, ["/", "/exit"])
    assert "PLAY" in text and "/play" in text


def test_the_palette_leads_with_the_daily_loop_not_the_alphabet(monkeypatch):
    text = _real_shell(monkeypatch, ["/", "/exit"])
    # Alphabetical put `arenas` and `autoplay` first and buried `play`. Position is the whole
    # point of the grouping, so position is what is asserted.
    assert text.index("/play") < text.index("/arenas")
    assert text.index("/play") < text.index("/autoplay")
    assert text.index("PLAY") < text.index("ACCOUNT")


def test_every_real_command_appears_somewhere_in_the_palette(monkeypatch):
    # The groups name ~25 commands by hand. A command added to the parser tomorrow must still
    # be listed — under MORE if no group claims it — or the palette silently hides features.
    text = _real_shell(monkeypatch, ["/", "/exit"])
    for name in shell._commands(cli.build_parser()):
        assert f"/{name}" in text, f"{name} is in the parser but missing from the palette"


def test_the_wordmark_has_no_escapes_without_colour():
    plain = shell._wordmark(color=False)
    assert "\x1b[" not in plain
    assert plain.count("\n") == 4, "the wordmark is five rows"


def test_the_wordmark_is_coloured_per_letter_not_mid_glyph():
    # A sweep applied per COLUMN lands its boundaries inside a stroke, which reads as a
    # rendering fault. Per letter means exactly one colour start per letter, per row.
    row = shell._wordmark(color=True).split("\n")[0]
    assert row.count("\x1b[38;5;") == len(shell._LETTERS)
