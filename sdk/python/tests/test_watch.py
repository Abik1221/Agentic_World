"""Tests for `pyyol watch` / `play` helpers — spectating is strictly read-only,
so these assert the SSE stream renders and that we resolve endpoints correctly.
There is intentionally no move-input path to test: the human never plays."""

import argparse

from pyyol import cli
from pyyol.credentials import Credentials


class RecordingConsole:
    def __init__(self):
        self.events = []

    def banner(self, *a):
        pass

    def emit(self, kind, msg, **fields):
        self.events.append((kind, msg))


def test_render_sse_parses_and_stops_on_terminal():
    console = RecordingConsole()
    # A real text/event-stream: id/event/data frames separated by blank lines,
    # with a keepalive comment, ending in a terminal event.
    stream = [
        b": keepalive\n",
        b"id: 1\n", b"event: round_revealed\n", b'data: {"seq":1,"card":7}\n', b"\n",
        b"id: 2\n", b"event: message\n", b'data: {"seq":2,"text":"hello"}\n', b"\n",
        b"id: 3\n", b"event: victory\n", b'data: {"seq":3,"winner":0}\n', b"\n",
        b"id: 4\n", b"event: after_end\n", b'data: {"seq":4}\n', b"\n",  # must NOT be rendered
    ]
    cli._render_sse(iter(stream), console)

    kinds = [k for k, _ in console.events]
    # Rendered up to and including victory, then a synthetic game_end, then stopped.
    assert kinds == ["round_revealed", "message", "victory", "game_end"]
    assert not any(k == "after_end" for k in kinds), "must stop at the terminal event"
    # The message summary surfaces a human-readable field.
    assert any("hello" in m for _, m in console.events)


def test_render_sse_handles_multiline_data_and_bad_json():
    console = RecordingConsole()
    stream = [
        b"event: chunk\n", b"data: not-json\n", b"\n",
    ]
    cli._render_sse(iter(stream), console)
    assert console.events and console.events[0][0] == "chunk"


def test_http_base_prefers_flag_then_derives_from_connect_url():
    creds = Credentials(connect_url="wss://arena.example:8443/v1/agent/connect")
    # Explicit --api wins.
    a = argparse.Namespace(api="https://x/api")
    assert cli._http_base(a, creds) == "https://x/api"
    # Otherwise derive http base from the WSS connect url (wss→https, drop path).
    a2 = argparse.Namespace(api="")
    assert cli._http_base(a2, creds) == "https://arena.example:8443"


def test_play_path_maps_each_game():
    assert cli._PLAY_PATH["goofspiel"] == "/v1/sandbox/pushplay"
    assert cli._PLAY_PATH["mafia"] == "/v1/mafia/pushplay"
    assert cli._PLAY_PATH["monopoly"] == "/v1/monopoly/pushplay"
