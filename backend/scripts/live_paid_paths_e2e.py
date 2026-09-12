#!/usr/bin/env python3
"""Live proof: local-SDK paid ranked + friend rooms, and hosted-away path.

Talks to BASE_URL (default http://localhost:8080). Never prints secrets.
Exit 0 only if all assertions pass. Prints YES/NO lines for the parent report.
"""
from __future__ import annotations

import base64
import json
import os
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

try:
    import websocket  # websocket-client
except ImportError:
    try:
        import websockets.sync.client as ws_sync
        websocket = None
    except ImportError:
        print("NEED: pip install websocket-client OR websockets", file=sys.stderr)
        sys.exit(2)

BASE = os.environ.get("BASE_URL", "http://localhost:8080").rstrip("/")
HOSTED_HOST = os.environ.get("HOSTED_STUB_HOST", "host.docker.internal")


def _http(method: str, path: str, token: str | None = None, body=None, scheme="Bearer"):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, method=method)
    if body is not None:
        req.add_header("Content-Type", "application/json")
    if token:
        req.add_header("Authorization", f"{scheme} {token}")
    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            raw = resp.read().decode() or "{}"
            return resp.status, json.loads(raw) if raw.strip() else {}
    except urllib.error.HTTPError as e:
        raw = e.read().decode() or "{}"
        try:
            payload = json.loads(raw) if raw.strip() else {}
        except json.JSONDecodeError:
            payload = {"raw": raw[:200]}
        return e.code, payload


def platform_token() -> str:
    """Ed25519 Platform token matching PLATFORM_ADMIN_PUBLIC_KEY in e2e."""
    from nacl.signing import SigningKey

    seed = b"pyyol-e2e-platform-token-seed!!!"
    sk = SigningKey(seed)
    now = int(time.time())
    payload = json.dumps(
        {"iss": "super-admin", "sub": "e2e-harness", "iat": now, "exp": now + 300},
        separators=(",", ":"),
    ).encode()
    sig = sk.sign(payload).signature
    return (
        base64.urlsafe_b64encode(payload).rstrip(b"=").decode()
        + "."
        + base64.urlsafe_b64encode(sig).rstrip(b"=").decode()
    )


def platform_token_stdlib() -> str:
    """Same token via cryptography/ed25519 if PyNaCl missing — use go helper fallback."""
    import hashlib
    import subprocess

    # Prefer the Go harness seed path already validated by unit tests.
    src = r'''
package main
import ("crypto/ed25519"; "encoding/base64"; "encoding/json"; "fmt"; "time")
func main() {
  priv := ed25519.NewKeyFromSeed([]byte("pyyol-e2e-platform-token-seed!!!"))
  now := time.Now().Unix()
  p,_ := json.Marshal(map[string]any{"iss":"super-admin","sub":"e2e-harness","iat":now,"exp":now+300})
  sig := ed25519.Sign(priv, p)
  fmt.Print(base64.RawURLEncoding.EncodeToString(p)+"."+base64.RawURLEncoding.EncodeToString(sig))
}
'''
    path = "/tmp/pyyol_plat_tok.go"
    open(path, "w").write(src)
    go = os.environ.get("GO_BIN", "go")
    out = subprocess.check_output([go, "run", path], stderr=subprocess.DEVNULL)
    return out.decode().strip()


def signup(name: str):
    uniq = f"{int(time.time()*1000)}-{hashlib_tag(name)}"
    st, body = _http(
        "POST",
        "/v1/auth/signup",
        body={
            "email": f"live+{uniq}@example.com",
            "password": "hunter2-strong-pass",
            "agent_name": name,
        },
    )
    if st != 201:
        raise RuntimeError(f"signup {name}: {st} {body}")
    dash = body["dashboard_token"]
    agent_id = body["agent_id"]
    # Signup no longer mints an unused play key — issue one for the WS path.
    st, keyed = _http(
        "POST",
        "/v1/agent/keys",
        dash,
        {"agent_id": agent_id, "label": f"e2e-{name}"},
    )
    if st != 201 or not keyed.get("api_key"):
        raise RuntimeError(f"issue key {name}: {st} {keyed}")
    return dash, keyed["api_key"], agent_id


def hashlib_tag(s: str) -> str:
    import hashlib

    return hashlib.sha1(s.encode()).hexdigest()[:8]


def mint(agent_id: str, amount: int, plat: str):
    st, body = _http(
        "POST",
        "/v1/admin/mint",
        token=plat,
        body={"agent": agent_id, "amount": amount},
        scheme="Platform",
    )
    if st != 200:
        raise RuntimeError(f"mint: {st} {body}")


class AgentSocket:
    """Minimal register-and-hold WebSocket so the gateway marks the agent connected."""

    def __init__(self, agent_id: str, api_key: str):
        self.agent_id = agent_id
        self.api_key = api_key
        self._stop = threading.Event()
        self._ok = threading.Event()
        self._err = None
        self._thr = threading.Thread(target=self._run, daemon=True)

    def start(self):
        self._thr.start()
        if not self._ok.wait(15):
            raise RuntimeError(f"socket register failed: {self._err or 'timeout'}")

    def close(self):
        self._stop.set()
        self._thr.join(timeout=5)

    def _run(self):
        url = BASE.replace("https://", "wss://").replace("http://", "ws://") + "/v1/agent/connect"
        try:
            if websocket is not None:
                self._run_ws_client(url)
            else:
                self._run_websockets(url)
        except Exception as e:
            self._err = e

    def _run_ws_client(self, url: str):
        ws = websocket.create_connection(url, timeout=10)
        try:
            hello = json.loads(ws.recv())
            if hello.get("t") != "hello":
                self._err = f"expected hello, got {hello}"
                return
            ws.send(
                json.dumps(
                    {
                        "t": "register",
                        "agent_id": self.agent_id,
                        "token": self.api_key,
                        "sdk_language": "python",
                        "sdk_version": "1.12.3",
                    }
                )
            )
            reg = json.loads(ws.recv())
            if reg.get("t") != "registered":
                self._err = f"register refused: {reg}"
                return
            self._ok.set()
            while not self._stop.wait(0.5):
                try:
                    ws.settimeout(0.2)
                    msg = ws.recv()
                    if not msg:
                        continue
                    frame = json.loads(msg)
                    if frame.get("t") == "ping":
                        ws.send(json.dumps({"t": "pong"}))
                    elif frame.get("t") == "turn":
                        # Legal goofspiel: play any legal card if present.
                        view = frame.get("view") or {}
                        legal = view.get("legal_actions") or view.get("legal") or []
                        card = legal[0] if legal else 1
                        ws.send(
                            json.dumps(
                                {
                                    "t": "response",
                                    "id": frame.get("id"),
                                    "action": {"round": view.get("round", 1), "card": card},
                                }
                            )
                        )
                except Exception:
                    pass
        finally:
            try:
                ws.close()
            except Exception:
                pass

    def _run_websockets(self, url: str):
        with ws_sync.connect(url, open_timeout=10) as ws:
            hello = json.loads(ws.recv())
            if hello.get("t") != "hello":
                self._err = f"expected hello, got {hello}"
                return
            ws.send(
                json.dumps(
                    {
                        "t": "register",
                        "agent_id": self.agent_id,
                        "token": self.api_key,
                        "sdk_language": "python",
                        "sdk_version": "1.12.3",
                    }
                )
            )
            reg = json.loads(ws.recv())
            if reg.get("t") != "registered":
                self._err = f"register refused: {reg}"
                return
            self._ok.set()
            while not self._stop.is_set():
                try:
                    msg = ws.recv(timeout=0.5)
                    frame = json.loads(msg)
                    if frame.get("t") == "ping":
                        ws.send(json.dumps({"t": "pong"}))
                except TimeoutError:
                    continue


def err_code(body) -> str:
    if not isinstance(body, dict):
        return ""
    e = body.get("error") or body
    if isinstance(e, dict):
        return str(e.get("code") or "")
    return str(body.get("code") or "")


def err_msg(body) -> str:
    if not isinstance(body, dict):
        return str(body)
    e = body.get("error") or body
    if isinstance(e, dict):
        return str(e.get("message") or e.get("code") or "")
    return str(body.get("message") or "")


class StubAgent(BaseHTTPRequestHandler):
    def log_message(self, *_a):
        return

    def do_GET(self):
        if self.path.startswith("/health"):
            self._json(200, {"status": "healthy", "agent": "HostedE2E", "version": "1.0.0"})
        elif self.path.startswith("/handshake"):
            self._json(
                200,
                {"accepted": True, "sdkVersion": "1.12.3", "supportedGames": ["goofspiel"]},
            )
        else:
            self.send_response(404)
            self.end_headers()

    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0)
        _ = self.rfile.read(n) if n else b""
        # /health + /handshake are siblings of /play (see manifest verify).
        if self.path.startswith("/handshake"):
            self._json(
                200,
                {
                    "accepted": True,
                    "sdkVersion": "1.12.3",
                    "supportedGames": ["goofspiel"],
                },
            )
            return
        # Accept play pushes for hosted path (turn delivery).
        self._json(200, {"ok": True, "action": {"round": 1, "card": 1}})

    def _json(self, code, obj):
        raw = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


def certify_hosted(dash: str, agent_id: str, endpoint_url: str):
    mdoc = {
        "manifestVersion": "1.0",
        "agent": {"name": "HostedE2E", "version": "1.0.0", "visibility": "public"},
        "developer": {"name": "Dev"},
        "games": ["goofspiel"],
        "endpoint": {"url": endpoint_url, "authentication": "bearer-token"},
        "runtime": {"timeout": 5000},
        "sdk": {"language": "Python"},
        "contact": {"email": "dev@example.com"},
    }
    st, sub = _http("POST", f"/v1/agents/{agent_id}/manifest", dash, mdoc)
    if st != 201:
        raise RuntimeError(f"manifest: {st} {sub}")
    mid = sub["manifest_id"]
    st, _ = _http(
        "PUT",
        f"/v1/agents/{agent_id}/manifest/{mid}/endpoint-secret",
        dash,
        {"token": "s"},
    )
    if st != 200:
        raise RuntimeError(f"secret: {st}")
    st, rep = _http("POST", f"/v1/agents/{agent_id}/manifest/{mid}/verify", dash, None)
    if st != 200 or not rep.get("verified"):
        raise RuntimeError(f"verify: {st} {rep}")


def main() -> int:
    results = {
        "local_ranked": False,
        "local_room": False,
        "hosted": False,
        "neither_refuses": False,
    }
    notes = []

    try:
        plat = platform_token_stdlib()
    except Exception as e:
        print(f"FAIL platform token: {e}", file=sys.stderr)
        return 1

    # ── Path A: local connected, no cert ──────────────────────────────────
    dash_a, key_a, ag_a = signup("LocalPaidA")
    dash_b, key_b, ag_b = signup("LocalPaidB")
    mint(ag_a, 100_000, plat)
    mint(ag_b, 100_000, plat)

    # Neither connected → ranked refuse
    st, body = _http("POST", "/v1/queue", dash_a, {"tier": "low"})
    if st in (409, 403) and err_code(body) in ("agent_not_playable", "agent_not_certified"):
        results["neither_refuses"] = True
        msg = err_msg(body)
        if "Verify your agent's endpoint" in msg:
            notes.append("WARN ranked refuse still uses certify-only copy")
        if "pyyol play" not in msg and "pyyol dev" not in msg:
            notes.append(f"WARN ranked refuse copy missing CLI hint: {msg[:120]}")
    else:
        notes.append(f"FAIL neither-reachable ranked: {st} {err_code(body)} {err_msg(body)[:120]}")

    sock_a = AgentSocket(ag_a, key_a)
    sock_b = AgentSocket(ag_b, key_b)
    sock_a.start()
    sock_b.start()
    time.sleep(0.5)  # gateway Connected() visibility

    st, body = _http("POST", "/v1/queue", dash_a, {"tier": "low"})
    if st == 202:
        results["local_ranked"] = True
        _http("DELETE", "/v1/queue", dash_a, None)
    else:
        notes.append(f"FAIL local ranked enqueue: {st} {err_code(body)} {err_msg(body)[:160]}")

    st, body = _http("POST", "/v1/room/create", dash_a, {"tier": "low"})
    if st in (200, 201):
        room_id = body.get("room_id") or body.get("match_id") or ""
        st2, body2 = _http("POST", "/v1/lobby/join", dash_b, {"match_id": room_id})
        if st2 == 200:
            results["local_room"] = True
        else:
            notes.append(f"FAIL room join: {st2} {err_code(body2)} {err_msg(body2)[:160]}")
    else:
        notes.append(f"FAIL room create: {st} {err_code(body)} {err_msg(body)[:160]}")
        if "Verify your agent's endpoint" in err_msg(body):
            notes.append("FAIL room gate still demands ranked endpoint verify")

    sock_a.close()
    sock_b.close()

    # ── Path B: hosted verify, no local socket ────────────────────────────
    stub = ThreadingHTTPServer(("0.0.0.0", 0), StubAgent)
    port = stub.server_address[1]
    thr = threading.Thread(target=stub.serve_forever, daemon=True)
    thr.start()
    endpoint = f"http://{HOSTED_HOST}:{port}/play"

    dash_h, key_h, ag_h = signup("HostedPaid")
    mint(ag_h, 100_000, plat)
    try:
        certify_hosted(dash_h, ag_h, endpoint)
        st, body = _http("POST", "/v1/queue", dash_h, {"tier": "low"})
        if st == 202:
            results["hosted"] = True
            _http("DELETE", "/v1/queue", dash_h, None)
        else:
            notes.append(f"FAIL hosted ranked: {st} {err_code(body)} {err_msg(body)[:160]}")
            # Fallback: if host.docker.internal failed, try 127.0.0.1 (host-run server)
            if HOSTED_HOST != "127.0.0.1":
                notes.append("hint: hosted stub may be unreachable from container; set HOSTED_STUB_HOST")
    except Exception as e:
        notes.append(f"FAIL hosted certify: {e}")
    finally:
        stub.shutdown()

    print("YES" if results["local_ranked"] else "NO", "- local SDK paid ranked without deploy")
    print("YES" if results["local_room"] else "NO", "- local SDK invite-friend money rooms without deploy")
    print("YES" if results["hosted"] else "NO", "- hosted deploy path still works")
    print("YES" if results["neither_refuses"] else "NO", "- neither connected nor hosted refuses")
    for n in notes:
        print(n)
    return 0 if all(results.values()) else 1


if __name__ == "__main__":
    sys.exit(main())
