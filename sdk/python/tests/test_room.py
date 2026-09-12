"""Private rooms are Goofspiel 1v1 — Mafia has no invite-room path yet."""

from __future__ import annotations

import argparse

import pyyol.cli as cli


def test_room_create_refuses_mafia(capsys, monkeypatch):
    # Do not hit the network — refuse before login/API.
    monkeypatch.setattr(cli, "_http_base", lambda *_a, **_k: "https://example.invalid")
    monkeypatch.setattr(cli, "_connection_token", lambda *_a, **_k: ("tok", "agent"))
    monkeypatch.setattr(cli, "_ensure_login", lambda *_a, **_k: object())

    ns = argparse.Namespace(
        action="create",
        id="",
        game="mafia",
        tier="low",
        bid=0,
        api="https://example.invalid",
        token="",
    )
    code = cli.cmd_room(ns)
    err = capsys.readouterr().err
    assert code == 2
    assert "Goofspiel (1v1) only" in err
    assert "12-seat" in err


def test_room_parser_accepts_game_flag():
    ns = cli.build_parser().parse_args(["room", "create", "--game", "goofspiel", "--tier", "low"])
    assert ns.game == "goofspiel"
    assert ns.tier == "low"
