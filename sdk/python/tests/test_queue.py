import argparse
import http.server
import json
import threading

from pyyol import cli, credentials


class _Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_a):  # silence
        pass

    def _json(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.startswith("/v1/games/"):  # public tier menu
            self._json(
                200,
                {
                    "game": "goofspiel",
                    "tiers": [
                        {"key": "low", "label": "Low", "coins": 100},
                        {"key": "mid", "label": "Mid", "coins": 500},
                    ],
                },
            )
        elif self.path == "/v1/queue":  # status poll → matched
            self._json(200, {"status": "matched", "match_id": "mt_test"})
        else:
            self._json(404, {})

    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(n)
        if self.path == "/v1/queue":
            self._json(202, {"status": "waiting"})
        else:
            self._json(404, {})


def _serve():
    srv = http.server.HTTPServer(("127.0.0.1", 0), _Handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, f"http://127.0.0.1:{srv.server_address[1]}"


def _args(**kw):
    d = dict(game="goofspiel", tier="", bid=0, list=False, wait=5.0, api="", token="")
    d.update(kw)
    return argparse.Namespace(**d)


def _no_keyring(monkeypatch, tmp_path):
    monkeypatch.setenv("PYYOL_HOME", str(tmp_path))
    monkeypatch.setattr(credentials, "_try_keyring", lambda: None)


def test_queue_list_prints_tiers(capsys, monkeypatch, tmp_path):
    _no_keyring(monkeypatch, tmp_path)
    srv, base = _serve()
    try:
        rc = cli.cmd_queue(_args(api=base, list=True))
    finally:
        srv.shutdown()
    out = capsys.readouterr().out
    assert rc == 0
    assert "mid" in out and "500" in out


def test_queue_enqueues_and_reports_match(capsys, monkeypatch, tmp_path):
    _no_keyring(monkeypatch, tmp_path)
    srv, base = _serve()
    try:
        rc = cli.cmd_queue(_args(api=base, tier="mid", token="agent-secret"))
    finally:
        srv.shutdown()
    out = capsys.readouterr().out
    assert rc == 0
    assert "matched" in out and "mt_test" in out


def test_queue_requires_a_stake(capsys, monkeypatch, tmp_path):
    _no_keyring(monkeypatch, tmp_path)
    srv, base = _serve()
    try:
        rc = cli.cmd_queue(_args(api=base, token="agent-secret"))  # no tier, no bid
    finally:
        srv.shutdown()
    assert rc == 2  # usage error: must choose --tier or --bid
