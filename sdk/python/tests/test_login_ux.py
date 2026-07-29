"""Terminal sign-in behaviour: the parts a first-time developer actually hits.

Covers the two failure modes that leave someone stuck with no idea what to do — a
browser that never opens, and a forged loopback callback that kills a real sign-in.
"""

import urllib.parse
import urllib.request

import pytest

from pyyol import credentials, login


@pytest.fixture
def home(tmp_path, monkeypatch):
    """Isolated credential store — same shape as test_auth_cli's fixture, so these
    tests never read or write the developer's real keychain."""
    monkeypatch.setenv("PYYOL_HOME", str(tmp_path))
    monkeypatch.setattr(credentials, "_try_keyring", lambda: None)
    return tmp_path


def test_auth_url_is_printed_when_the_browser_cannot_open(home, capsys):
    """webbrowser.open() returns False (or lies) over SSH, in WSL, and in containers.

    Without the URL on screen the user watches a silent prompt until the timeout with
    nothing to act on. Every mature CLI prints the link; so do we.
    """

    def dead_opener(_auth_url):
        return False  # a browser that cannot be launched

    with pytest.raises(TimeoutError):
        login.run_login_flow("https://dash.example", timeout=1, _opener=dead_opener)

    err = capsys.readouterr().err
    assert "/cli-login" in err, "the sign-in URL must be printed so it can be pasted"
    assert "callback=" in err, "the printed URL must be the complete, usable one"


def test_timeout_message_says_what_to_do(home):
    """A bare 'timed out' teaches nothing. The message names the cause and the fix."""

    with pytest.raises(TimeoutError) as e:
        login.run_login_flow("https://dash.example", timeout=1, _opener=lambda _u: False)
    msg = str(e.value)
    assert "timed out" in msg
    assert "browser" in msg, "say WHERE the response was expected from"


def test_forged_callback_cannot_abort_a_real_login(home):
    """A wrong-state callback is ignored, not fatal.

    It never leaked credentials — the CSRF check already gated that — but it used to
    end the wait, so any local process that reached the loopback port first could kill
    a legitimate sign-in. Here the forged hit lands, is refused, and the REAL callback
    that follows still succeeds.
    """

    def opener(auth_url):
        q = urllib.parse.parse_qs(urllib.parse.urlsplit(auth_url).query)
        callback, state = q["callback"][0], q["state"][0]
        # An attacker gets there first with the wrong state.
        try:
            urllib.request.urlopen(f"{callback}?token=evil&state=WRONG", timeout=5).read()
        except Exception:
            pass
        # The genuine redirect follows and must still complete the login.
        urllib.request.urlopen(f"{callback}?token=real&state={state}", timeout=5).read()

    creds = login.run_login_flow("https://dash.example", timeout=10, _opener=opener)
    assert creds.access_token == "real", "the forged callback must not win or abort"
