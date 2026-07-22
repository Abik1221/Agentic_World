import argparse

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
