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
        login.run_login_flow(
            "https://pyyol.com",
            api_url="https://api.pyyol.com",
            timeout=0.2,
            _opener=lambda url: False,
        )
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
        assert "#000" in html, "must use the black console canvas"
        assert "aria-label=\"pyyol\"" in html, "must carry the product lockup"
        assert "viewport" in html, "must render on a phone"
        assert "Times" not in html
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


def _run_callback(home, query: str, timeout: float = 10.0):
    """Drive the loopback with a chosen callback query string and return Credentials."""
    import urllib.parse
    import urllib.request

    from pyyol import login

    def opener(auth_url: str) -> bool:
        # Stand in for the dashboard: read the CSRF state it handed us, then call back.
        q = urllib.parse.parse_qs(urllib.parse.urlsplit(auth_url).query)
        cb, state = q["callback"][0], q["state"][0]
        urllib.request.urlopen(f"{cb}?state={state}&{query}", timeout=5).read()
        return True

    return login.run_login_flow(
        "https://pyyol.com", api_url="https://api.pyyol.com", timeout=timeout, _opener=opener
    )


def test_dashboard_token_and_agent_key_are_stored_separately(home):
    """The two credentials must not be conflated.

    The dashboard used to return the AGENT key as `token`, so the CLI stored it as
    access_token — which is what owner commands send. `pyyol publish` then
    authenticated as the agent and got 403 agent_cannot_modify_limits, making ranked
    play unreachable from the CLI at all.
    """
    creds = _run_callback(home, "token=dash-jwt-abc&api_key=sk_arena_xyz&agent_id=ag_1")

    assert creds.access_token == "dash-jwt-abc", "owner credential must land in access_token"
    assert creds.api_key == "sk_arena_xyz", "agent key must land in api_key"
    assert creds.access_token != creds.api_key, "the two credentials must never be the same value"


def test_login_still_works_against_a_dashboard_that_sends_only_one(home):
    """During a rollout the deployed frontend may send either field alone. Refusing
    the callback would break login for everyone until the frontend caught up."""
    only_key = _run_callback(home, "api_key=sk_arena_only&agent_id=ag_2")
    assert only_key.api_key == "sk_arena_only"

    only_tok = _run_callback(home, "token=dash-only&agent_id=ag_3")
    assert only_tok.access_token == "dash-only"


def test_a_callback_with_neither_credential_is_rejected(home):
    """State alone must not be enough — otherwise an empty callback 'succeeds'.

    A credential-less callback is answered 400 and ignored, so the flow keeps waiting
    for a real one and ends in TimeoutError. Asserting the specific type matters: a
    blind `Exception` here would also pass if the flow crashed for some unrelated
    reason, which is the opposite of what this is checking.
    """
    import pytest

    with pytest.raises(TimeoutError):
        _run_callback(home, "agent_id=ag_4", timeout=1.0)


def test_login_names_the_key_after_this_machine(home, monkeypatch):
    """The auth URL must carry a device label.

    Agent keys are one-per-label and issuing replaces only the matching label
    (backend migration 0071). Without a label the dashboard falls back to a shared
    per-client-kind name, which puts every machine back in one slot — the very
    behaviour that used to make a second `pyyol login` revoke the first machine's key
    and knock a running deployment offline.
    """
    monkeypatch.setattr(login.socket, "gethostname", lambda: "Studio-Mini.local")
    seen: dict = {}

    def opener(auth_url: str) -> bool:
        q = urllib.parse.parse_qs(urllib.parse.urlsplit(auth_url).query)
        seen.update(label=(q.get("label") or [""])[0])
        cb, state = q["callback"][0], q["state"][0]
        urllib.request.urlopen(f"{cb}?state={state}&token=t&api_key=sk_arena_x_y", timeout=5).read()
        return True

    login.run_login_flow("https://pyyol.com", timeout=10, _opener=opener)
    # The mDNS suffix is stripped so the label reads as the machine's name.
    assert seen["label"] == "Studio-Mini"


def test_device_label_is_stable_and_machine_specific(monkeypatch):
    """Stability and distinctness are both load-bearing.

    Not stable ⇒ every login ADDS a key instead of replacing its own, until the agent
    hits the 20-live-key cap. Not distinct ⇒ logging in on a laptop revokes a
    server's key. A hostname is both; a random id or a constant breaks one of them.
    """
    monkeypatch.setattr(login.socket, "gethostname", lambda: "ci-runner-7")
    assert login.device_label() == login.device_label() == "ci-runner-7"
    monkeypatch.setattr(login.socket, "gethostname", lambda: "laptop")
    assert login.device_label() == "laptop"


def test_device_label_falls_back_when_the_hostname_is_unavailable(monkeypatch):
    """No hostname is not a reason to fail a login — but the label must still be a
    valid, non-empty label the backend will accept."""

    def boom():
        raise OSError("no hostname")

    monkeypatch.setattr(login.socket, "gethostname", boom)
    assert login.device_label() == "pyyol cli"
    monkeypatch.setattr(login.socket, "gethostname", lambda: "   ")
    assert login.device_label() == "pyyol cli"
