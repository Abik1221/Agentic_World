import argparse

import pytest

from pyyol import cli, credentials


def _args(token=""):
    return argparse.Namespace(token=token)


def test_prefers_agent_key_over_dashboard_jwt():
    creds = credentials.Credentials(access_token="jwt-abc", api_key="sk_arena_key123")
    token, using_key = cli._connection_token(_args(), creds)
    assert token == "sk_arena_key123"  # the long-lived key wins
    assert using_key is True


def test_falls_back_to_jwt_when_no_key():
    creds = credentials.Credentials(access_token="jwt-abc", api_key="")
    token, using_key = cli._connection_token(_args(), creds)
    assert token == "jwt-abc"
    assert using_key is False  # JWT path → connector will auto-refresh


def test_explicit_agent_key_flag_is_detected():
    token, using_key = cli._connection_token(_args(token="sk_arena_explicit"), None)
    assert token == "sk_arena_explicit" and using_key is True


def test_explicit_non_key_token_uses_refresh_path():
    token, using_key = cli._connection_token(_args(token="raw-jwt"), None)
    assert token == "raw-jwt" and using_key is False


def test_credentials_round_trip_agent_key(monkeypatch, tmp_path):
    monkeypatch.setenv("PYYOL_HOME", str(tmp_path))
    monkeypatch.setattr(credentials, "_try_keyring", lambda: None)  # force file backend
    credentials.save(
        credentials.Credentials(
            url="https://api.pyyol.com",
            agent_id="ag_1",
            access_token="jwt",
            refresh_token="rt",
            api_key="sk_arena_persisted",
        )
    )
    loaded = credentials.load()
    assert loaded is not None
    assert loaded.api_key == "sk_arena_persisted"  # the persistent key survives a reload
    assert loaded.access_token == "jwt" and loaded.refresh_token == "rt"


# --- a revoked agent key must be terminal, not refreshed around --------------------


class _FakeWS:
    """Minimal websocket stand-in: replays queued frames, records what was sent."""

    def __init__(self, incoming):
        import json
        import queue

        self._in = queue.Queue()
        for f in incoming:
            self._in.put(json.dumps(f))
        self.sent = []

    def send(self, msg):
        import json

        self.sent.append(json.loads(msg))

    def recv(self, *_a, **_k):
        import queue

        try:
            return self._in.get_nowait()
        except queue.Empty:
            raise ConnectionError("closed")

    def close(self):
        pass


def _connector(agent, ws, **kw):
    from pyyol.runtime import RuntimeConnector

    return RuntimeConnector(
        agent,
        url="ws://x",
        agent_id="ag",
        token="sk_arena_dead_key",
        games=["goofspiel"],
        heartbeat_interval=100,
        _connect=lambda *a, **k: ws,
        **kw,
    )


def _agent():
    from pyyol import Agent

    a = Agent(supported_games=["goofspiel"], name="t")

    @a.on_turn("goofspiel")
    def decide(_v):
        return {"card": 1}

    return a


def test_revoked_key_is_terminal_and_never_refreshed_around(monkeypatch):
    """A revoked agent key must stop the agent, not silently downgrade it.

    The refresh reflex on a rejected register swaps the long-lived sk_arena_… key for a
    short-lived dashboard JWT, which registers fine. The agent then keeps playing while
    the dead key stays in the keyring, so every restart repeats a failed register
    forever and the developer is never told the credential they deployed is gone.
    """
    from pyyol.runtime import ConnectorError

    ws = _FakeWS(
        [
            {"t": "hello", "version": "1.0"},
            {"t": "error", "error": "key_revoked", "reason": "this agent key was revoked — …"},
        ]
    )
    refreshed = {"n": 0}

    conn = _connector(
        _agent(),
        ws,
        refresh_token="rt",
        api_url="https://api.example",
    )

    def _no(*_a, **_k):
        refreshed["n"] += 1
        return ("new-access", "new-refresh")

    monkeypatch.setattr(conn, "_refresh_http", _no)

    try:
        conn._session()
    except ConnectorError as e:
        assert "revoked" in str(e).lower(), e
        assert "pyyol login" in str(e), "the error must say how to recover"
    else:
        raise AssertionError("a revoked key must raise ConnectorError, not continue")

    assert refreshed["n"] == 0, "a revoked key must NOT spend the refresh token"
    assert conn.token == "sk_arena_dead_key", "the connection token must not be swapped"


def test_other_register_rejections_still_refresh(monkeypatch):
    """The revoked-key branch must not swallow the case it was carved out of: an
    expired access token still refreshes and retries, which is what keeps a
    long-running agent alive without a re-login."""
    from pyyol.runtime import _RefreshRetry

    ws = _FakeWS(
        [
            {"t": "hello", "version": "1.0"},
            {"t": "error", "error": "unauthorized", "reason": "register token rejected"},
        ]
    )
    conn = _connector(_agent(), ws, refresh_token="rt", api_url="https://api.example")
    monkeypatch.setattr(conn, "_refresh_http", lambda *_a, **_k: ("new-access", "new-refresh"))

    with pytest.raises(_RefreshRetry):
        conn._session()
    assert conn.token == "new-access"
