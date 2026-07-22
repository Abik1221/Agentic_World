import argparse
import http.server
import json
import threading

from pyyol import cli, credentials


class _Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_a):  # silence
        pass

    def do_GET(self):
        if self.path == "/v1/user/wallet":
            body = json.dumps(
                {
                    "available_balance": 12345,
                    "locked_balance": 200,
                    "lifetime_earnings": 5000,
                    "coin_cents": 1,
                    "agents": [
                        {"name": "OlympAI", "agent": "ag_1", "balance": 900, "withdrawable": 400},
                    ],
                }
            ).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        else:
            self.send_response(404)
            self.end_headers()


def _serve():
    srv = http.server.HTTPServer(("127.0.0.1", 0), _Handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, f"http://127.0.0.1:{srv.server_address[1]}"


def _login(monkeypatch, tmp_path):
    monkeypatch.setenv("PYYOL_HOME", str(tmp_path))
    monkeypatch.setattr(credentials, "_try_keyring", lambda: None)
    credentials.save(credentials.Credentials(url="http://x", access_token="tok", agent_id="ag_1"))


def test_wallet_prints_balance_and_agents(capsys, monkeypatch, tmp_path):
    _login(monkeypatch, tmp_path)
    srv, base = _serve()
    try:
        rc = cli.cmd_wallet(argparse.Namespace(api=base, json=False))
    finally:
        srv.shutdown()
    out = capsys.readouterr().out
    assert rc == 0
    assert "12,345 coins" in out  # available balance, formatted
    assert "OlympAI" in out and "withdrawable 400" in out


def test_wallet_requires_login(capsys, monkeypatch, tmp_path):
    monkeypatch.setenv("PYYOL_HOME", str(tmp_path))
    monkeypatch.setattr(credentials, "_try_keyring", lambda: None)
    rc = cli.cmd_wallet(argparse.Namespace(api="", json=False))
    assert rc == 2  # not logged in
