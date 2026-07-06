"""Credential storage + browser login-flow tests (no network, no keyring needed —
the file fallback path, which is what CI exercises)."""

import urllib.parse
import urllib.request

import pytest

from onavion import credentials, login


@pytest.fixture
def home(tmp_path, monkeypatch):
    monkeypatch.setenv("ONAVION_HOME", str(tmp_path))
    # Force the file fallback so the test is deterministic regardless of a host keyring.
    monkeypatch.setattr(credentials, "_try_keyring", lambda: None)
    return tmp_path


def test_save_load_clear_roundtrip(home):
    assert credentials.load() is None
    creds = credentials.Credentials(
        url="https://host/api", connect_url="wss://host/v1/agent/connect",
        agent_id="ag_1", access_token="tok", refresh_token="ref")
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
    assert login.derive_connect_url("http://localhost:8080") == "ws://localhost:8080/v1/agent/connect"
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

    creds = login.run_login_flow("https://dash.example", api_url="https://host/api",
                                 timeout=5, _opener=fake_opener)
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
