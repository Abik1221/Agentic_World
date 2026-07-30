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


def test_waiting_message_names_the_wait_and_its_limit(home, capsys):
    """A silent terminal is indistinguishable from a hung one.

    The user has just been sent to a browser; if they miss the tab, the only thing
    telling them the CLI is alive and blocked on THEM is this line.
    """
    from pyyol import login

    try:
        login.run_login_flow("https://pyyol.com", api_url="https://api.pyyol.com",
                             timeout=0.2, _opener=lambda url: False)
    except Exception:
        pass
    err = capsys.readouterr().err
    assert "waiting" in err.lower(), err
    assert "cancel" in err.lower(), "must say how to get out of the wait"


def test_loopback_pages_are_branded_and_self_contained():
    """These pages are the last screen of the sign-in flow.

    Unstyled default-serif HTML immediately after a branded dashboard reads as a
    broken redirect. They must also reference NO external asset: this is a throwaway
    loopback server with no network the page can depend on.
    """
    from pyyol import login

    for page in (login._OK_PAGE, login._BAD_PAGE):
        html = page.decode("utf-8")
        assert "<style>" in html, "page must carry its own styling"
        assert "#0b0b0f" in html, "must use the platform canvas colour"
        assert "viewport" in html, "must render on a phone"
        # No external fetches — nothing to break, nothing to leak the loopback URL to.
        for bad in ("http://", "https://", "<script", "<img"):
            assert bad not in html, f"loopback page must not contain {bad!r}"


def test_success_page_tells_the_user_to_return_to_the_terminal():
    from pyyol import login

    html = login._OK_PAGE.decode("utf-8").lower()
    assert "terminal" in html
    # And the failure page must NOT imply anything was signed in.
    bad = login._BAD_PAGE.decode("utf-8").lower()
    assert "nothing was signed in" in bad
