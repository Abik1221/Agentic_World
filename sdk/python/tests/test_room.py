"""Private rooms: Goofspiel 1v1 and Mafia (12 invited humans, no house bots)."""

from __future__ import annotations

import argparse

import pyyol.cli as cli


def test_room_create_accepts_mafia(capsys, monkeypatch):
    posted: dict = {}

    def fake_post(url, token, body):
        posted["url"] = url
        posted["body"] = body
        return 201, {"room_id": "mf_room1", "match_id": "mf_room1", "game": "mafia", "bid": 100}

    monkeypatch.setattr(cli, "_http_base", lambda *_a, **_k: "https://example.invalid")
    monkeypatch.setattr(cli, "_connection_token", lambda *_a, **_k: ("tok", "agent"))
    monkeypatch.setattr(cli, "_ensure_login", lambda *_a, **_k: object())
    monkeypatch.setattr(cli, "_api_post", fake_post)

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
    out = capsys.readouterr().out
    assert code == 0
    assert posted["body"] == {"game": "mafia", "tier": "low"}
    assert "mf_room1" in out
    assert "invited agents only" in out.lower()
    assert "house bots fill" not in out.lower()


def test_room_create_refuses_unknown_game(capsys, monkeypatch):
    monkeypatch.setattr(cli, "_http_base", lambda *_a, **_k: "https://example.invalid")
    monkeypatch.setattr(cli, "_connection_token", lambda *_a, **_k: ("tok", "agent"))
    monkeypatch.setattr(cli, "_ensure_login", lambda *_a, **_k: object())

    ns = argparse.Namespace(
        action="create",
        id="",
        game="monopoly",
        tier="low",
        bid=0,
        api="https://example.invalid",
        token="",
    )
    code = cli.cmd_room(ns)
    err = capsys.readouterr().err
    assert code == 2
    assert "goofspiel" in err.lower()
    assert "mafia" in err.lower()


def test_room_parser_accepts_game_flag():
    ns = cli.build_parser().parse_args(["room", "create", "--game", "mafia", "--tier", "low"])
    assert ns.game == "mafia"
    assert ns.tier == "low"
