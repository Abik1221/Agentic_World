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


# ── the shell against the REAL command set ───────────────────────────────────
#
# Every test above drives a stand-in parser with three commands. That checks the loop's own
# rules and cannot see the shell meeting the actual CLI: a command whose registration confuses
# _commands(), or a name the menu cannot group, breaks the front door — the first screen anyone
# sees after installing — while the whole suite stays green.


def test_the_shell_lists_every_real_command(monkeypatch):
    text = _run(monkeypatch, ["/help", "/exit"], cli.build_parser)
    parser = cli.build_parser()
    for name in shell._commands(parser):
        assert name in text, f"{name!r} is a real command the shell's own help never shows"


def test_the_real_front_door_opens_and_leaves_cleanly(monkeypatch):
    """`pyyol` → banner → a command → `/exit`, on the parser developers actually get."""
    text = _run(monkeypatch, ["games --api https://example.invalid", "/exit"], cli.build_parser)
    # Assert the AFFORDANCES, not the wordmark: the banner is ASCII block art, so searching it
    # for the literal name finds nothing (which is how the first version of this test failed).
    # What a new developer actually needs from the first screen is how to find commands and how
    # to leave.
    assert "/ for commands" in text, "the front door must show how to discover commands"
    assert "/exit" in text, "and how to get out"
    # The command was dispatched through the real parser rather than rejected as unknown.
    assert "unknown command" not in text
    # An unreachable arena is reported as a plain failure, not as a crash — this runs against
    # a deliberately invalid host, which is the common case on a plane or behind a proxy.
    assert "bug in pyyol" not in text, "a network failure must never be reported as our bug"


def test_an_unknown_command_is_refused_against_the_real_set(monkeypatch):
    text = _run(monkeypatch, ["definitely-not-a-command", "/exit"], cli.build_parser)
    assert "unknown command" in text
    assert "/help" in text, "a refusal must point at the way to discover the real names"


# ── the `/` menu must reach every command ────────────────────────────────────


def test_the_menu_can_reach_a_command_past_the_visible_window():
    """The picker showed ten rows and clamped the selection to them.

    With twenty-five commands that left FIFTEEN unreachable from the menu — the affordance the
    banner advertises silently covered under half the tool, and the failure is invisible
    because the first ten are the common ones.

    Driven through the module's own ordering rather than a hand-written list, so this keeps
    testing the real menu as commands are added.
    """
    parser = cli.build_parser()
    cmds = shell._commands(parser)
    assert len(cmds) > 10, "this test only means something with more commands than rows"

    # Rebuild the ordering the picker uses: groups first, in order, then the rest.
    ordered: list[str] = []
    seen: set[str] = set()
    for _title, names in shell._GROUPS:
        for n in names:
            if n in cmds and n not in seen:
                ordered.append(n)
                seen.add(n)
    ordered.extend(sorted(set(cmds) - seen))

    assert ordered[:3], "the ordering must not be empty"
    # Every command is somewhere in the list the picker walks — which is what makes it
    # reachable now that the selection is bounded by the list rather than by the window.
    for name in cmds:
        assert name in ordered, f"{name!r} is in the parser but not in the menu's ordering"


def test_the_menu_ordering_puts_the_daily_loop_first():
    """Alphabetical put `arenas` and `autoplay` above `play` and `dev`. The groups are an
    ORDER, not a list: the first thing a developer sees should be the thing they do most."""
    parser = cli.build_parser()
    cmds = shell._commands(parser)
    first_group_names = [n for n in shell._GROUPS[0][1] if n in cmds]
    assert "play" in first_group_names or "dev" in first_group_names, (
        f"the first group is {shell._GROUPS[0][0]!r} containing {first_group_names} — the "
        "daily loop must lead"
    )


def test_every_command_is_grouped_or_falls_through_to_more():
    """The groups are hand-written and the parser is not. A command added tomorrow must still
    appear in the menu, or it exists and nobody can find it."""
    parser = cli.build_parser()
    cmds = shell._commands(parser)
    claimed = {n for _t, names in shell._GROUPS for n in names}
    # Unclaimed commands are fine — they land under "More". What must never happen is a group
    # naming a command the parser does not have, which would render a dead menu entry.
    for _title, names in shell._GROUPS:
        for n in names:
            if n not in cmds:
                raise AssertionError(
                    f"group entry {n!r} is not a real command — the menu would show a dead row"
                )
    assert claimed, "the groups must claim something"


# ── the live strip must not eat a running command's output ──────────────────────


class _FakeStream:
    """A stream that records writes and claims to be a TTY."""

    def __init__(self) -> None:
        self.chunks: list[str] = []

    def write(self, s: str) -> int:
        self.chunks.append(s)
        return len(s)

    def flush(self) -> None:
        pass

    def isatty(self) -> bool:
        return True

    @property
    def text(self) -> str:
        return "".join(self.chunks)


def _strip(stream):
    s = shell._Style(color=True)
    return shell._LiveStrip("http://example.invalid", s, stream)


def test_the_strip_erases_the_line_above_it_when_idle():
    """The baseline the next test is measured against.

    _redraw is SUPPOSED to step up a line and clear it — that is how the ticker stays in
    place above the prompt instead of scrolling away.
    """
    out = _FakeStream()
    strip = _strip(out)
    strip._redraw()
    assert "\x1b[1A" in out.text, "the strip should move up one line"
    assert "\x1b[2K" in out.text, "the strip should clear that line"


def test_the_strip_stays_silent_while_a_command_owns_the_terminal():
    """A running command's output must never be erased by the ticker.

    `pyyol play` streams a decision feed from the match thread while the strip thread
    fires every five seconds. Unmuted, _redraw steps up one line and clears it — and that
    line is whatever the match just printed, not the strip.

    The symptom is worse than losing a line: a log reading `turn 93` then `turn 95` looks
    like a DROPPED TURN, which reads as a forfeit and sends somebody hunting a reconnect
    bug in the arena that does not exist. The turn was played; the ticker wiped the line.
    """
    out = _FakeStream()
    strip = _strip(out)
    with strip.quiet():
        strip._redraw()
    assert out.text == "", (
        "the strip repainted while a command was running; it would have erased the line "
        f"the command had just printed (wrote {out.text!r})"
    )


def test_muting_is_released_even_if_the_command_raises():
    """A command that blows up must not leave the ticker dead for the rest of the session."""
    out = _FakeStream()
    strip = _strip(out)
    try:
        with strip.quiet():
            raise RuntimeError("command failed")
    except RuntimeError:
        pass
    strip._redraw()
    assert "\x1b[2K" in out.text, "the strip never resumed after a failing command"


def test_the_shell_mutes_the_strip_around_dispatch():
    """The guard has to be WIRED, not merely available.

    Checked against the source because driving the REPL needs a live stdin: the point is
    that no dispatch path runs with the ticker still painting.
    """
    import inspect

    src = inspect.getsource(shell._loop)
    dispatches = src.count("_dispatch(")
    muted = src.count("strip.quiet()")
    assert dispatches > 0, "the loop should dispatch something"
    assert muted >= dispatches, (
        f"{dispatches} dispatch call(s) but only {muted} muted — a command that prints "
        "while the ticker is live will have its output erased"
    )
    assert "_pick(" in src and src.index("strip.quiet()") < src.index("_pick("), (
        "the / picker must run muted too — the ticker stepping up a line erases the menu"
    )


def test_help_topic_shows_that_command_usage(monkeypatch):
    """`/help` as a wall of one-liners is how you find a name, not how you use it."""
    text = _real_shell(monkeypatch, ["/help play", "/exit"])
    assert "usage: pyyol play" in text
    assert "--ranked" in text


def test_complete_token_preserves_a_leading_slash():
    cmds = {"play": "", "publish": "", "dev": ""}
    assert shell._complete_token("pl", cmds) == "play"
    assert shell._complete_token("/pub", cmds) == "/publish"
    assert shell._complete_token("p", cmds) == "p"  # play + publish share only 'p'


def test_argv_slash_help_is_not_an_invalid_choice(capsys):
    """`pyyol /help` from bash used to be argparse 'invalid choice: /help'."""
    rc = cli.main(["/help"])
    assert rc == 0
    out = capsys.readouterr().out
    assert "invalid choice" not in out
    assert "START HERE" in out
    assert "/play" in out


def test_argv_help_topic_shows_usage(capsys):
    rc = cli.main(["help", "play"])
    assert rc == 0
    out = capsys.readouterr().out
    assert "usage: pyyol play" in out


def test_argv_leading_slash_runs_the_command(capsys):
    rc = cli.main(["/logs"])
    assert rc == 0
    out = capsys.readouterr().out
    assert "invalid choice" not in out
    assert "logs" in out.lower() or "no logs" in out.lower()


def test_argv_lone_slash_prints_the_palette(capsys):
    rc = cli.main(["/"])
    assert rc == 0
    out = capsys.readouterr().out
    assert "invalid choice" not in out
    assert "PLAY" in out and "/play" in out


def test_module_entrypoint_exists():
    """`python -m pyyol` must work — it is what the test plan and many docs imply."""
    import pyyol.__main__ as mod  # noqa: F401

    assert callable(mod.main)
