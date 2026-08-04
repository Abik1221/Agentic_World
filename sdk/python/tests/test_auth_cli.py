"""Credential storage + browser login-flow tests (no network, no keyring needed —
the file fallback path, which is what CI exercises)."""

import urllib.parse
import urllib.request

import pytest

from pyyol import credentials, login


@pytest.fixture
def home(tmp_path, monkeypatch):
    monkeypatch.setenv("PYYOL_HOME", str(tmp_path))
    # Force the file fallback so the test is deterministic regardless of a host keyring.
    monkeypatch.setattr(credentials, "_try_keyring", lambda: None)
    return tmp_path


def test_save_load_clear_roundtrip(home):
    assert credentials.load() is None
    creds = credentials.Credentials(
        url="https://host/api",
        connect_url="wss://host/v1/agent/connect",
        agent_id="ag_1",
        access_token="tok",
        refresh_token="ref",
    )
    backend = credentials.save(creds)
    assert backend == "file"

    loaded = credentials.load()
    assert loaded is not None
    assert loaded.access_token == "tok"
    assert loaded.agent_id == "ag_1"
    assert loaded.connect_url == "wss://host/v1/agent/connect"

    # File must be private (0600).
    import os
    import stat

    mode = stat.S_IMODE(os.stat(home / "credentials.json").st_mode)
    assert mode == 0o600

    assert credentials.clear() is True
    assert credentials.load() is None


def test_derive_connect_url():
    assert login.derive_connect_url("https://host/api") == "wss://host/v1/agent/connect"
    assert (
        login.derive_connect_url("http://localhost:8080") == "ws://localhost:8080/v1/agent/connect"
    )
    assert login.derive_connect_url("") == ""


def test_login_flow_captures_token_over_loopback(home):
    # The opener simulates the dashboard: it reads the callback + state from the
    # auth URL and redirects back to the loopback with a token, exactly as the
    # /cli-login page would after authenticating the user.
    def fake_opener(auth_url):
        q = urllib.parse.parse_qs(urllib.parse.urlsplit(auth_url).query)
        callback = q["callback"][0]
        state = q["state"][0]
        cb = f"{callback}?token=cli-token-xyz&state={state}&agent_id=ag_99&connect_url=wss://host/v1/agent/connect"
        urllib.request.urlopen(cb, timeout=5).read()

    creds = login.run_login_flow(
        "https://dash.example", api_url="https://host/api", timeout=5, _opener=fake_opener
    )
    assert creds.access_token == "cli-token-xyz"
    assert creds.agent_id == "ag_99"
    assert creds.connect_url == "wss://host/v1/agent/connect"


def test_login_flow_rejects_state_mismatch(home):
    def bad_opener(auth_url):
        q = urllib.parse.parse_qs(urllib.parse.urlsplit(auth_url).query)
        callback = q["callback"][0]
        cb = f"{callback}?token=x&state=WRONG"  # CSRF state mismatch
        try:
            urllib.request.urlopen(cb, timeout=5).read()
        except Exception:
            pass

    with pytest.raises(TimeoutError):
        login.run_login_flow("https://dash.example", timeout=2, _opener=bad_opener)


# --- owner-token refresh (publish, wallet, limits) ---------------------------------


def _creds(tmp_path, monkeypatch, access="stale-jwt", refresh="rt"):
    from pyyol import credentials

    monkeypatch.setenv("PYYOL_HOME", str(tmp_path))
    monkeypatch.setattr(credentials, "_try_keyring", lambda: None)
    c = credentials.Credentials(
        url="https://api.example", agent_id="ag_1", access_token=access, refresh_token=refresh
    )
    credentials.save(c)
    return c


def test_owner_token_refreshes_a_stale_dashboard_jwt(tmp_path, monkeypatch):
    """`pyyol publish` is the required step before a ranked match, and it read
    creds.access_token raw. The dashboard JWT is short-lived, so a developer who logged in
    in the morning and published in the afternoon sent an expired token and was told to log
    in again — on the one path that leads to competing for real."""
    from pyyol import cli, credentials

    c = _creds(tmp_path, monkeypatch)
    seen = {}

    def fake_post(url, token, body):
        seen.update(url=url, body=body)
        # The field this endpoint really returns. Reading the wrong key would fall through
        # to the stored token and the refresh would silently never happen.
        return 200, {"dashboard_token": "fresh-jwt", "refresh_token": "rt2"}

    monkeypatch.setattr(cli, "_api_post", fake_post)
    assert cli._owner_token(c) == "fresh-jwt"
    assert seen["url"].endswith("/v1/auth/refresh")
    assert seen["body"] == {"refresh_token": "rt"}
    # Persisted, so the NEXT command starts fresh instead of refreshing again — and the
    # rotated refresh token is kept, or a server that rotates them would end the session.
    stored = credentials.load()
    assert stored.access_token == "fresh-jwt"
    assert stored.refresh_token == "rt2"


def test_owner_token_falls_back_to_the_stored_token_when_refresh_fails(tmp_path, monkeypatch):
    """A failed refresh must not swallow the command: it still runs and still reports the
    server's own error, which is actionable, rather than a refresh failure that is not."""
    from pyyol import cli

    c = _creds(tmp_path, monkeypatch)
    monkeypatch.setattr(cli, "_api_post", lambda *_a, **_k: (401, {"error": "expired"}))
    assert cli._owner_token(c) == "stale-jwt"


def test_owner_token_prefers_an_explicit_flag_and_never_refreshes(tmp_path, monkeypatch):
    """--token is the caller overriding us on purpose (CI); refreshing around it would
    use a credential they did not ask for."""
    from pyyol import cli

    c = _creds(tmp_path, monkeypatch)

    def boom(*_a, **_k):
        raise AssertionError("must not refresh when --token was given")

    monkeypatch.setattr(cli, "_api_post", boom)
    assert cli._owner_token(c, "explicit") == "explicit"


def test_owner_token_without_a_refresh_token_is_a_no_op(tmp_path, monkeypatch):
    """No refresh token (e.g. `pyyol login --token`) must not attempt a refresh call."""
    from pyyol import cli

    c = _creds(tmp_path, monkeypatch, refresh="")

    def boom(*_a, **_k):
        raise AssertionError("must not refresh without a refresh token")

    monkeypatch.setattr(cli, "_api_post", boom)
    assert cli._owner_token(c) == "stale-jwt"
    assert cli._owner_token(None) == ""
