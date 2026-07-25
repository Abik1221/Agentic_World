"""The anonymous install ping: fires once per version, dedups, and honors opt-out."""

from __future__ import annotations

import json
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from pyyol import install_ping


class _Capture(BaseHTTPRequestHandler):
    posts: list = []

    def do_POST(self):  # noqa: N802
        length = int(self.headers.get("Content-Length", 0))
        _Capture.posts.append(json.loads(self.rfile.read(length)))
        self.send_response(204)
        self.end_headers()

    def log_message(self, *a):  # silence
        pass


@pytest.fixture()
def server():
    _Capture.posts = []
    httpd = HTTPServer(("127.0.0.1", 0), _Capture)
    t = threading.Thread(target=httpd.serve_forever, daemon=True)
    t.start()
    yield f"http://127.0.0.1:{httpd.server_address[1]}"
    httpd.shutdown()


@pytest.fixture(autouse=True)
def _telemetry_on(monkeypatch, tmp_path):
    # conftest disables telemetry globally; re-enable it here + isolate the marker dir.
    monkeypatch.delenv("PYYOL_NO_TELEMETRY", raising=False)
    monkeypatch.delenv("DO_NOT_TRACK", raising=False)
    monkeypatch.setenv("PYYOL_HOME", str(tmp_path))


def _wait_posts(n=1, timeout=2.0):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if len(_Capture.posts) >= n:
            return
        time.sleep(0.02)


def test_fires_once_with_sdk_and_version(server, tmp_path):
    install_ping.maybe_ping(server, "9.9.9")
    _wait_posts(1)
    assert _Capture.posts == [{"sdk": "python", "version": "9.9.9"}]
    assert os.path.exists(os.path.join(str(tmp_path), ".install_pinged_9.9.9"))

    # Second call for the same version → deduped (no new POST).
    install_ping.maybe_ping(server, "9.9.9")
    time.sleep(0.2)
    assert len(_Capture.posts) == 1


def test_opt_out_env_suppresses(server, monkeypatch):
    monkeypatch.setenv("DO_NOT_TRACK", "1")
    install_ping.maybe_ping(server, "9.9.9")
    time.sleep(0.2)
    assert _Capture.posts == []


def test_no_api_base_is_noop(server):
    install_ping.maybe_ping("", "9.9.9")
    time.sleep(0.1)
    assert _Capture.posts == []
