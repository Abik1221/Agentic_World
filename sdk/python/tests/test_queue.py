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
# Prefer the stored agent key when both exist. A dashboard-only login (JWT, no key)
# must still queue — the server sits the owned agent.


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
        f"when both credentials exist the long-lived agent key must win. Sent: {sent!r}"
    )
    assert "dashboard-jwt-NOT-agent-scope" not in sent, (
        "the agent key should win over the dashboard JWT when both are stored"
    )


def test_queue_sends_the_dashboard_jwt_when_there_is_no_agent_key(monkeypatch, tmp_path):
    _no_keyring(monkeypatch, tmp_path)
    srv, base = _serve_capturing()
    credentials.save(
        credentials.Credentials(
            url=base,
            agent_id="ag_1",
            access_token="dashboard-jwt-only",
        )
    )
    try:
        rc = cli.cmd_queue(_args(api=base, tier="mid"))
    finally:
        srv.shutdown()

    assert rc == 0
    sent = " ".join(_AuthCapturingHandler.seen_auth)
    assert "dashboard-jwt-only" in sent, (
        f"a dashboard-only login must still be able to queue. Sent: {sent!r}"
    )


def test_queue_still_honours_an_explicit_token(monkeypatch, tmp_path):
    # CI and scripted runs pass a credential directly; that must keep winning over
    # whatever happens to be stored on the machine.
    _no_keyring(monkeypatch, tmp_path)
    srv, base = _serve_capturing()
    credentials.save(credentials.Credentials(url=base, agent_id="ag_1", api_key="sk_arena_stored"))
    try:
        rc = cli.cmd_queue(_args(api=base, tier="mid", token="sk_arena_explicit"))
    finally:
        srv.shutdown()

    assert rc == 0
    sent = " ".join(_AuthCapturingHandler.seen_auth)
    assert "sk_arena_explicit" in sent and "sk_arena_stored" not in sent


class _CertThenQueueHandler(_Handler):
    queue_posts = 0

    def do_GET(self):
        if "/manifest" in self.path:
            self._json(200, {})
            return
        super().do_GET()

    def do_POST(self):
        n = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(n)
        if self.path.endswith("/manifest"):
            self._json(201, {"manifest_id": "mf_1"})
            return
        if self.path.endswith("/verify"):
            self._json(200, {"verified": True})
            return
        if self.path == "/v1/queue":
            _CertThenQueueHandler.queue_posts += 1
            if _CertThenQueueHandler.queue_posts == 1:
                self._json(403, {"code": "agent_not_certified"})
                return
            self._json(202, {"status": "waiting"})
            return
        self._json(404, {})


def test_queue_certifies_a_connected_manifest_then_retries(monkeypatch, tmp_path):
    _no_keyring(monkeypatch, tmp_path)
    _CertThenQueueHandler.queue_posts = 0
    srv = http.server.HTTPServer(("127.0.0.1", 0), _CertThenQueueHandler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    base = f"http://127.0.0.1:{srv.server_address[1]}"
    credentials.save(
        credentials.Credentials(url=base, agent_id="ag_1", access_token="dashboard-jwt-only")
    )
    try:
        rc = cli.cmd_queue(_args(api=base, tier="mid"))
    finally:
        srv.shutdown()
    assert rc == 0
    assert _CertThenQueueHandler.queue_posts == 2


def test_every_queue_flag_cmd_queue_reads_is_actually_defined(monkeypatch, tmp_path):
    """Parse REAL argv, don't hand-build the Namespace.

    cmd_queue polls for a pairing and read `args.wait`, but nothing ever defined the
    flag — so the command crashed on every single run with
    `AttributeError: 'Namespace' object has no attribute 'wait'`, printed immediately
    after "✓ queued". The enqueue had already succeeded, so the agent was genuinely in
    the queue while the developer was told the tool had broken.

    Every other test in this file builds a Namespace by hand and supplies wait=5.0
    itself, an attribute the parser never produced. That is exactly why this shipped:
    the fixture invented the interface it was testing.

    So this one goes through the real parser.
    """
    from pyyol.cli import build_parser

    ns = build_parser().parse_args(["queue", "goofspiel", "--tier", "low"])
    for attr in ("game", "tier", "bid", "list", "wait", "token", "api"):
        assert hasattr(ns, attr), (
            f"cmd_queue reads args.{attr}, but the parser does not define it — "
            f"the command will crash with AttributeError on every run"
        )
