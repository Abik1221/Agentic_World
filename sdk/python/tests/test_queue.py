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


# ── Which credential the queue sends ────────────────────────────────────────
#
# /v1/queue is registered server-side with RequireScope(ScopeAgent), so it needs the
# AGENT key. cmd_queue sent the dashboard session token instead, and every ranked queue
# attempt came back:
#
#   403 forbidden_scope: This credential is not allowed to access this resource
#
# For every developer, every time — and `pyyol queue <game> --tier low` is the command
# the scaffold prints as THE way to play ranked, so ranked matchmaking was unreachable
# from the CLI.
#
# It was invisible because every existing test above passes token="agent-secret"
# explicitly, which short-circuits the credential choice. Nothing exercised the path a
# real developer takes: `pyyol login`, then `pyyol queue`.

class _AuthCapturingHandler(_Handler):
    seen_auth: list[str] = []

    def do_POST(self):
        _AuthCapturingHandler.seen_auth.append(self.headers.get("Authorization", ""))
        super().do_POST()


def _serve_capturing():
    _AuthCapturingHandler.seen_auth = []
    srv = http.server.HTTPServer(("127.0.0.1", 0), _AuthCapturingHandler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, f"http://127.0.0.1:{srv.server_address[1]}"


def test_queue_sends_the_agent_key_not_the_dashboard_token(monkeypatch, tmp_path):
    _no_keyring(monkeypatch, tmp_path)
    srv, base = _serve_capturing()
    credentials.save(
        credentials.Credentials(
            url=base,
            agent_id="ag_1",
            access_token="dashboard-jwt-NOT-agent-scope",
            api_key="sk_arena_theagentkey",
        )
    )
    try:
        # No explicit token: exactly what a developer gets after `pyyol login`.
        rc = cli.cmd_queue(_args(api=base, tier="mid"))
    finally:
        srv.shutdown()

    assert rc == 0
    sent = " ".join(_AuthCapturingHandler.seen_auth)
    assert "sk_arena_theagentkey" in sent, (
        "the queue call did not carry the agent key — the server requires agent scope "
        f"and will answer 403 forbidden_scope. Sent: {sent!r}"
    )
    assert "dashboard-jwt-NOT-agent-scope" not in sent, (
        "the queue call carried the dashboard session token, which the server rejects"
    )


def test_queue_still_honours_an_explicit_token(monkeypatch, tmp_path):
    # CI and scripted runs pass a credential directly; that must keep winning over
    # whatever happens to be stored on the machine.
    _no_keyring(monkeypatch, tmp_path)
    srv, base = _serve_capturing()
    credentials.save(
        credentials.Credentials(url=base, agent_id="ag_1", api_key="sk_arena_stored")
    )
    try:
        rc = cli.cmd_queue(_args(api=base, tier="mid", token="sk_arena_explicit"))
    finally:
        srv.shutdown()

    assert rc == 0
    sent = " ".join(_AuthCapturingHandler.seen_auth)
    assert "sk_arena_explicit" in sent and "sk_arena_stored" not in sent
