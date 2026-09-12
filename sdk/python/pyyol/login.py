"""The ``pyyol login`` browser flow.

Opens the platform login page in the browser and captures the issued token on a
local loopback callback — the developer never copies a key by hand. The CLI:

1. starts a throwaway HTTP server on ``127.0.0.1:<random port>``,
2. opens ``{dashboard}/cli-login?callback=<loopback>&state=<nonce>&label=<hostname>``
   in the browser,
3. the dashboard authenticates the user and redirects back to the loopback with
   ``?token=…&agent_id=…&state=…``,
4. the CLI validates ``state`` (CSRF) and stores the credentials.

The frontend must serve the ``/cli-login`` page that performs step 3; the CLI
side (loopback capture + secure storage) is complete and lives here.
"""

from __future__ import annotations

import hmac
import re
import secrets
import socket
import sys
import threading
import urllib.parse
import webbrowser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from .credentials import Credentials


# Keep the markup in lockstep with sdk/js/src/login.ts — both CLIs serve this
# same loopback page. Unstyled default-serif HTML (the JS 1.12.2 page) read as a
# broken redirect after the branded dashboard. Self-contained: no network, no
# <img>, no xmlns (tests forbid "http://"). The lockup is the product mark from
# /pyyol-logo.png — pixel cascade + word — drawn inline so it cannot 404.
_LOCKUP = (
    '<svg class=lockup viewBox="0 0 200 48" role="img" aria-label="pyyol">'
    '<g fill="#7eb3ff">'
    '<rect x="0" y="36" width="7" height="7" rx="1.5"/>'
    '<rect x="9.2" y="36" width="7" height="7" rx="1.5"/>'
    '<rect x="6.2" y="26.6" width="6.4" height="6.4" rx="1.4"/>'
    '<rect x="15.2" y="26.6" width="6.4" height="6.4" rx="1.4"/>'
    '<rect x="13" y="18.2" width="5.6" height="5.6" rx="1.3"/>'
    '<rect x="21" y="18.2" width="5.6" height="5.6" rx="1.3"/>'
    '<rect x="19.4" y="11.2" width="4.6" height="4.6" rx="1.15"/>'
    '<rect x="26.2" y="11.2" width="4.6" height="4.6" rx="1.15"/>'
    '<rect x="25.2" y="5.6" width="3.6" height="3.6" rx="1"/>'
    '<rect x="30.6" y="5.6" width="3.6" height="3.6" rx="1"/>'
    '<rect x="30.2" y="1.6" width="2.5" height="2.5" rx=".75"/>'
    '<rect x="34.2" y="1.6" width="2.5" height="2.5" rx=".75"/>'
    '<rect x="34.4" y="0" width="1.6" height="1.6" rx=".5"/>'
    "</g>"
    '<text x="46" y="40" fill="#e8e9ed" font-size="28" font-weight="500" '
    "letter-spacing=\"-0.04em\" "
    "font-family=\"Space Grotesk,ui-sans-serif,system-ui,-apple-system,'Segoe UI',sans-serif\">"
    "pyyol</text></svg>"
)


def _page(title: str, heading: str, copy: str) -> bytes:
    return (
        "<!doctype html><html lang=en><meta charset=utf-8>"
        "<meta name=viewport content='width=device-width,initial-scale=1'>"
        f"<title>{title} · pyyol</title>"
        "<style>"
        ":root{color-scheme:dark}"
        "*{box-sizing:border-box}"
        "html,body{margin:0;min-height:100%;background:#000;color:#e8e9ed;"
        "font:15px/1.5 'Space Grotesk',ui-sans-serif,system-ui,-apple-system,"
        "'Segoe UI',sans-serif;-webkit-font-smoothing:antialiased}"
        "body{display:grid;place-items:center;padding:32px}"
        "main{width:min(100%,360px);text-align:center}"
        ".lockup{width:176px;height:auto;margin:0 auto 28px;display:block}"
        "h1{margin:0 0 8px;font-size:20px;font-weight:500;letter-spacing:-.03em}"
        "p{margin:0;color:#8b8d96;font-size:14px}"
        "</style>"
        f"<body><main>{_LOCKUP}<h1>{heading}</h1><p>{copy}</p></main>"
    ).encode()


_OK_PAGE = _page(
    "Signed in",
    "Signed in",
    "Return to your terminal. You can close this tab.",
)
_BAD_PAGE = _page(
    "Sign-in failed",
    "Sign-in didn&rsquo;t complete",
    "Nothing was signed in. Return to your terminal and run the command again.",
)


def derive_connect_url(api_url: str) -> str:
    """Derive the WSS connect URL from a platform API/base URL.

    ``https://host/api`` -> ``wss://host/v1/agent/connect``;
    ``http://localhost:8080`` -> ``ws://localhost:8080/v1/agent/connect``."""
    if not api_url:
        return ""
    u = urllib.parse.urlsplit(api_url)
    scheme = "wss" if u.scheme in ("https", "wss") else "ws"
    return urllib.parse.urlunsplit((scheme, u.netloc, "/v1/agent/connect", "", ""))


def device_label() -> str:
    """A stable name for THIS machine, used to label the agent key issued to it.

    Agent keys are one-per-machine and re-issuing for the same label replaces that
    machine's key (see backend migration 0071). So this must be stable across logins
    on one machine — otherwise every login would add a key instead of replacing the
    one it supersedes — and distinct between machines, or logging in on a laptop
    would revoke a server's key. The hostname is both; a random id would break the
    first property and a constant would break the second.
    """
    name = ""
    try:
        name = socket.gethostname()
    except Exception:  # noqa: BLE001 — no hostname is not a reason to fail login
        name = ""
    # Strip the mDNS suffix macOS adds ("mbp.local") so the label matches what the
    # developer calls the machine.
    name = re.sub(r"\.local$", "", (name or "").strip(), flags=re.IGNORECASE)
    return name or "pyyol cli"


def run_login_flow(
    dashboard_url: str,
    api_url: str = "",
    timeout: float = 180.0,
    provider: str = "",
    label: str = "",
    _opener=None,
) -> Credentials:
    """Run the loopback browser login and return captured Credentials.

    ``label`` names the key issued to this device (defaults to the hostname); it is
    what the owner sees — and revokes — in the dashboard key list.

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
            api_key = (q.get("api_key") or [""])[0]
            # Accept the callback if EITHER credential came back.
            #
            # The dashboard now returns both: `token` (the developer's session, for
            # owner-scope commands like publish) and `api_key` (the narrow agent key
            # that plays matches). Requiring `token` specifically would break against
            # a dashboard that only sends one of them — which is exactly the state
            # every already-deployed frontend is in during a rollout.
            ok = bool(token or api_key) and hmac.compare_digest((q.get("state") or [""])[0], state)
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
        # Name the key after this machine so the dashboard list is readable and one
        # machine's re-login cannot evict another's key.
        auth_url += f"&label={urllib.parse.quote(label or device_label(), safe='')}"

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
            (
                "opening your browser to sign in…"
                if opened
                else "couldn't open a browser automatically."
            ),
            file=sys.stderr,
        )
        print(f"  if it didn't open, visit:\n  {auth_url}\n", file=sys.stderr)
        # Without this the terminal sits silent for up to three minutes and a user who
        # missed the browser tab cannot tell whether the CLI is working, hung, or done.
        # Naming the wait — and its limit — is the difference between "it's waiting on
        # me" and "it's broken". Same reason gh/vercel/stripe all print it.
        print(
            f"waiting for you to finish signing in… (up to {int(timeout)}s; Ctrl-C to cancel)",
            file=sys.stderr,
        )

        if not done.wait(timeout):
            raise TimeoutError(
                f"login timed out after {int(timeout)}s — no response came back from the browser. "
                "Open the URL above and finish signing in, then run the command again."
            )
        if not (captured.get("token") or captured.get("api_key")):
            raise RuntimeError("login was cancelled or rejected in the browser")
    finally:
        httpd.shutdown()
        httpd.server_close()

    return Credentials(
        url=api_url,
        connect_url=captured.get("connect_url") or derive_connect_url(api_url),
        agent_id=captured.get("agent_id", ""),
        access_token=captured.get("token", ""),
        refresh_token=captured.get("refresh", ""),
        api_key=captured.get("api_key", ""),
    )
