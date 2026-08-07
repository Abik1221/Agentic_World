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


# The loopback pages are the LAST thing a developer sees in the sign-in flow, right
# after a branded dashboard. Served as unstyled default-serif HTML they read as a
# broken redirect or a phishing intercept rather than as the product — so they carry
# the platform palette (see Pyyol_client/app/globals.css) and say plainly what to do
# next. Self-contained by necessity: this is a throwaway loopback server with no
# static assets and no network the page can rely on.
def _page(title: str, body: str, accent: str) -> bytes:
    return (
        "<!doctype html><html lang=en><meta charset=utf-8>"
        "<meta name=viewport content='width=device-width,initial-scale=1'>"
        f"<title>{title} · pyyol</title>"
        "<style>"
        ":root{color-scheme:dark}"
        "*{box-sizing:border-box}"
        "body{margin:0;min-height:100vh;display:flex;align-items:center;"
        "justify-content:center;padding:24px;background:#0b0b0f;color:#e2e2ea;"
        "font:15px/1.6 ui-sans-serif,-apple-system,'Segoe UI',Roboto,sans-serif}"
        ".card{width:100%;max-width:420px;background:#111118;border:1px solid #2a2a37;"
        "border-radius:16px;padding:32px;text-align:center}"
        ".dot{width:44px;height:44px;margin:0 auto 20px;border-radius:50%;"
        f"display:flex;align-items:center;justify-content:center;background:{accent}22;"
        f"border:1px solid {accent}55;font-size:20px;color:{accent}}}"
        "h1{margin:0 0 8px;font-size:18px;font-weight:600;letter-spacing:-.01em}"
        "p{margin:0;color:#8d8da1;font-size:13.5px}"
        ".mark{margin-top:24px;padding-top:18px;border-top:1px solid #2a2a37;"
        "font:11px/1 ui-monospace,SFMono-Regular,Menlo,monospace;letter-spacing:.16em;"
        "text-transform:uppercase;color:#5a5a70}"
        "</style>"
        f"<body><main class=card>{body}<div class=mark>pyyol</div></main>"
    ).encode()


_OK_PAGE = _page(
    "Signed in",
    "<div class=dot>&#10003;</div><h1>You&rsquo;re signed in</h1>"
    "<p>You can close this tab and return to your terminal.</p>",
    "#34d399",
)
_BAD_PAGE = _page(
    "Sign-in failed",
    "<div class=dot>&#33;</div><h1>Sign-in didn&rsquo;t complete</h1>"
    "<p>The request couldn&rsquo;t be verified, so nothing was signed in. "
    "Return to your terminal and run the command again.</p>",
    "#f59e0b",
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
