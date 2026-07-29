"""The ``pyyol login`` browser flow.

Opens the platform login page in the browser and captures the issued token on a
local loopback callback — the developer never copies a key by hand. The CLI:

1. starts a throwaway HTTP server on ``127.0.0.1:<random port>``,
2. opens ``{dashboard}/cli-login?callback=<loopback>&state=<nonce>`` in the browser,
3. the dashboard authenticates the user and redirects back to the loopback with
   ``?token=…&agent_id=…&state=…``,
4. the CLI validates ``state`` (CSRF) and stores the credentials.

The frontend must serve the ``/cli-login`` page that performs step 3; the CLI
side (loopback capture + secure storage) is complete and lives here.
"""

from __future__ import annotations

import hmac
import secrets
import sys
import threading
import urllib.parse
import webbrowser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from .credentials import Credentials

_OK_PAGE = b"<!doctype html><meta charset=utf-8><h2>pyyol: login complete \xe2\x9c\x93</h2><p>You can close this tab and return to your terminal.</p>"
_BAD_PAGE = b"<!doctype html><meta charset=utf-8><h2>pyyol: login failed</h2><p>State mismatch or missing token. Try again.</p>"


def derive_connect_url(api_url: str) -> str:
    """Derive the WSS connect URL from a platform API/base URL.

    ``https://host/api`` -> ``wss://host/v1/agent/connect``;
    ``http://localhost:8080`` -> ``ws://localhost:8080/v1/agent/connect``."""
    if not api_url:
        return ""
    u = urllib.parse.urlsplit(api_url)
    scheme = "wss" if u.scheme in ("https", "wss") else "ws"
    return urllib.parse.urlunsplit((scheme, u.netloc, "/v1/agent/connect", "", ""))


def run_login_flow(
    dashboard_url: str,
    api_url: str = "",
    timeout: float = 180.0,
    provider: str = "",
    _opener=None,
) -> Credentials:
    """Run the loopback browser login and return captured Credentials.

    ``_opener(url)`` overrides how the auth URL is opened (tests inject a function
    that simulates the dashboard redirect back to the loopback)."""
    state = secrets.token_urlsafe(16)
    captured: dict = {}
    done = threading.Event()

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):  # noqa: N802
            parsed = urllib.parse.urlsplit(self.path)
            if parsed.path != "/callback":
                self.send_response(404)
                self.end_headers()
                return
            q = urllib.parse.parse_qs(parsed.query)
            token = (q.get("token") or [""])[0]
            # Constant-time CSRF-state check (mirrors the signing module's discipline).
            ok = bool(token) and hmac.compare_digest((q.get("state") or [""])[0], state)
            self.send_response(200 if ok else 400)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.end_headers()
            self.wfile.write(_OK_PAGE if ok else _BAD_PAGE)
            if ok:
                captured.update(
                    token=token,
                    agent_id=(q.get("agent_id") or [""])[0],
                    connect_url=(q.get("connect_url") or [""])[0],
                    refresh=(q.get("refresh_token") or [""])[0],
                    # Optional: the dashboard may hand back a long-lived agent key
                    # directly. If it doesn't, `pyyol login` mints one post-auth.
                    api_key=(q.get("api_key") or [""])[0],
                )
                # Only a callback that PASSES the CSRF check ends the wait.
                #
                # This used to fire unconditionally, so anything that could reach the
                # loopback port — any local process, any page doing a cross-origin GET
                # at the right moment — could abort a legitimate sign-in just by
                # arriving first with a wrong state. It never leaked credentials (the
                # state check already gated that), but it made the login trivially
                # killable. A forged callback is now answered 400 and ignored, and the
                # real one still completes.
                done.set()

        def log_message(self, *_a):  # silence the default stderr logging
            pass

    httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    port = httpd.server_address[1]
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    try:
        callback = f"http://127.0.0.1:{port}/callback"
        auth_url = (
            f"{dashboard_url.rstrip('/')}/cli-login"
            f"?callback={urllib.parse.quote(callback, safe='')}&state={state}"
        )
        if provider:  # let the dashboard pre-select GitHub/Google/wallet
            auth_url += f"&provider={urllib.parse.quote(provider, safe='')}"

        # ALWAYS print the URL, then try to open it.
        #
        # webbrowser.open() returns False — or worse, True having done nothing — over
        # SSH, in WSL, in containers, and on headless boxes. Without the URL on screen
        # the user sat watching a silent prompt until a 3-minute timeout, with no way
        # to know what was expected of them or that anything had failed. Every mature
        # CLI (gh, wrangler, vercel, stripe) prints the link for exactly this reason.
        #
        # It is printed BEFORE the open attempt so it is visible even if opening
        # raises, and the loopback port is already listening by this point, so a user
        # who pastes it into a browser on the same machine completes normally.
        opened = False
        try:
            opened = bool((_opener or webbrowser.open)(auth_url))
        except Exception:  # noqa: BLE001 — a browser we cannot launch is not fatal
            opened = False
        # Printed unconditionally, including under an injected opener: gating this on
        # "are we in a test" would mean the tested path is not the shipped one, and
        # this message is the whole safety net for a browser that never appears.
        print(
            ("opening your browser to sign in…" if opened else "couldn't open a browser automatically."),
            file=sys.stderr,
        )
        print(f"  if it didn't open, visit:\n  {auth_url}\n", file=sys.stderr)

        if not done.wait(timeout):
            raise TimeoutError(
                f"login timed out after {int(timeout)}s — no response came back from the browser. "
                "Open the URL above and finish signing in, then run the command again."
            )
        if not captured.get("token"):
            raise RuntimeError("login was cancelled or rejected in the browser")
    finally:
        httpd.shutdown()
        httpd.server_close()

    return Credentials(
        url=api_url,
        connect_url=captured.get("connect_url") or derive_connect_url(api_url),
        agent_id=captured.get("agent_id", ""),
        access_token=captured["token"],
        refresh_token=captured.get("refresh", ""),
        api_key=captured.get("api_key", ""),
    )
