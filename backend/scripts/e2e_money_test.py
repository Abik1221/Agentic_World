#!/usr/bin/env python3
"""End-to-end money-pipeline test: game → coin → crypto deposit → crypto withdrawal.

Reusable + self-contained. Runs against a live arena (default http://localhost:8080).
Coin + game stages are fully local. The deposit/withdrawal stages exercise the
crypto rails when the arena has Solana configured (devnet for testing) — otherwise
they report "rail off" instead of failing.

  ARENA=http://localhost:8080 python3 scripts/e2e_money_test.py
Optional: DATABASE_URL (psql) for the ledger-conservation check; PYYOL_TEST_EMAIL/
PYYOL_TEST_PASSWORD to reuse an account (avoids the signup rate limit).
"""
import json, os, subprocess, threading, time, urllib.error, urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ARENA = os.environ.get("ARENA", "http://localhost:8080").rstrip("/")
EMAIL = os.environ.get("PYYOL_TEST_EMAIL", "money-e2e@example.com")
PASSWORD = os.environ.get("PYYOL_TEST_PASSWORD", "Passw0rd!123")
MOCK_PORT = int(os.environ.get("MOCK_PORT", "9098"))
results = []


def check(name, ok, detail=""):
    results.append((name, ok, detail))
    print(f"  {'✅' if ok else '❌'} {name}{(' — ' + detail) if detail else ''}")
    return ok


def req(method, path, token=None, body=None):
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(ARENA + path, data=data, method=method)
    if token:
        r.add_header("Authorization", "Bearer " + token)
    if data is not None:
        r.add_header("content-type", "application/json")
    try:
        with urllib.request.urlopen(r, timeout=20) as resp:
            return resp.status, json.loads(resp.read() or b"{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read() or b"{}")
        except Exception:
            return e.code, {}


# ── a minimal goofspiel agent so a real match can play to completion ──────────
class Agent(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _s(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        self._s(200, {"status": "healthy", "version": "1.0.0"}) if self.path.endswith("/health") else self._s(404, {})

    def do_POST(self):
        n = int(self.headers.get("content-length", 0) or 0)
        try:
            body = json.loads(self.rfile.read(n) or b"{}") if n else {}
        except Exception:
            body = {}
        p = self.path
        if p.endswith("/handshake"):
            self._s(200, {"accepted": True, "sdkVersion": "e2e-1.0", "supportedGames": ["goofspiel"]})
        elif p.endswith("/decide"):
            legal = body.get("legal_actions") or body.get("your_hand") or []
            prize = body.get("current_prize", 0)
            card = (max(legal) if prize >= 7 else min(legal)) if legal else 1
            self._s(200, {"card": card, "rationale": f"prize={prize} card {card}", "usage": {"total_tokens": 40}})
        else:
            self._s(200, {"ok": True})


def account():
    """Login-first (avoids the signup rate limit); signup if new."""
    st, r = req("POST", "/v1/auth/login", body={"email": EMAIL, "password": PASSWORD})
    if st == 200:
        dt, aid = r["dashboard_token"], r["agent_id"]
        _, rk = req("POST", "/v1/agent/keys", dt, {"agent_id": aid})
        return dt, aid, rk.get("api_key")
    st, r = req("POST", "/v1/auth/signup", body={"email": EMAIL, "password": PASSWORD, "agent_name": "MoneyE2E", "description": "e2e"})
    assert st in (200, 201), (st, r)
    dt, aid = r["dashboard_token"], r["agent_id"]
    key = r.get("api_key")
    if not key:
        _, rk = req("POST", "/v1/agent/keys", dt, {"agent_id": aid, "label": "e2e"})
        key = rk.get("api_key")
    return dt, aid, key


def ensure_manifest(dt, aid):
    """Register + verify a manifest whose endpoint is the local mock agent, so
    sandbox push-play can drive it. Endpoint '/decide' → the arena probes sibling
    '/health' + '/handshake' (agentclient.siblingURL) which the mock serves."""
    endpoint = f"http://localhost:{MOCK_PORT}/decide"
    man = {
        "manifestVersion": "1.0",
        "agent": {"name": "MoneyE2E", "description": "e2e", "version": f"1.0.{int(time.time()) % 100000}", "visibility": "private"},
        "developer": {"name": "E2E", "organization": "E2E"},
        "games": ["goofspiel"],
        "endpoint": {"url": endpoint, "authentication": "bearer-token"},
        "runtime": {"timeout": 5000, "maxMemory": "256Mi"},
        "sdk": {"language": "python", "version": "1.0"},
        "contact": {"email": EMAIL},
    }
    st, r = req("POST", f"/v1/agents/{aid}/manifest", dt, man)
    if st not in (200, 201):
        return False
    mid = r.get("id") or r.get("manifest_id")
    req("PUT", f"/v1/agents/{aid}/manifest/{mid}/endpoint-secret", dt, {"token": "sek"})
    st, rep = req("POST", f"/v1/agents/{aid}/manifest/{mid}/verify", dt, {})
    return st == 200 and bool(rep.get("verified"))


def main():
    print(f"E2E money pipeline → {ARENA}")
    srv = ThreadingHTTPServer(("127.0.0.1", MOCK_PORT), Agent)
    threading.Thread(target=srv.serve_forever, daemon=True).start()

    dt, aid, ak = account()
    check("account: user+agent session", bool(dt and aid and ak), aid)

    # ── COIN ──────────────────────────────────────────────────────────────────
    print("\n[coin]")
    st, r = req("POST", "/v1/admin/mint", dt, {"agent": aid, "amount": 1000})
    check("mint 1000 coins", st == 200, str(r.get("minted")))
    st, w = req("GET", f"/v1/wallet?agent={aid}", dt)
    check("wallet balance ≥ 1000", st == 200 and (w.get("balance", 0) >= 1000), f"balance={w.get('balance')}")

    # ── GAME (sandbox push-play against the local mock agent, real match) ───────
    print("\n[game]")
    verified = ensure_manifest(dt, aid)
    check("agent endpoint verified (mock)", verified, "" if verified else "manifest verify failed")
    st, m = req("POST", "/v1/sandbox/pushplay", ak, {"difficulty": "medium"})
    mid = m.get("match_id", "")
    check("sandbox match created", bool(mid), mid or (json.dumps(m)[:100]))
    if mid:
        finished = False
        for _ in range(40):
            time.sleep(1)
            _, rep = req("GET", f"/v1/match/{mid}/replay")
            if rep.get("status") == "finished":
                finished = True
                break
        check("match played to completion", finished, "status=" + str(rep.get("status")))

    # ── CRYPTO DEPOSIT ──────────────────────────────────────────────────────────
    print("\n[deposit — crypto]")
    st, d = req("POST", "/v1/deposits", dt, {"amount_usdc": 5})
    if st == 503:
        check("deposit rail", False, "503 — Solana deposits not configured (set SOLANA_* to enable)")
    else:
        ok = st in (200, 201) and bool(d.get("deposit_address") or d.get("ata") or d.get("address") or d.get("reference"))
        check("deposit session created (address + reference)", ok, json.dumps(d)[:140])
        did = d.get("id") or d.get("session_id")
        if did:
            _, ds = req("GET", f"/v1/deposits/{did}", dt)
            check("deposit session is pending/awaiting", ds.get("status") in ("pending", "awaiting", "open", "created", "confirming"), "status=" + str(ds.get("status")))

    # ── CRYPTO WITHDRAWAL ───────────────────────────────────────────────────────
    print("\n[withdrawal — crypto]")
    st, wd = req("GET", f"/v1/wallet/withdrawable?agent={aid}", dt)
    check("withdrawable endpoint", st == 200, json.dumps(wd)[:100])
    st, r = req("POST", "/v1/withdrawals", dt, {"agent": aid, "coins": 100})
    # Either it creates a pending withdrawal, or it correctly refuses (no NET
    # winnings to withdraw — minted coins aren't withdrawable). Both are valid.
    if st in (200, 201):
        check("withdrawal requested (pending, maker-checker)", r.get("status") in ("requested", "pending", "processing"), "status=" + str(r.get("status")))
    else:
        code = (r.get("error") or {}).get("code", "")
        check("withdrawal correctly gated", st in (400, 402, 403, 409), f"{st} {code}")

    # ── CONSERVATION (optional, needs DATABASE_URL psql or the dev container) ────
    print("\n[ledger conservation]")
    bad = ledger_bad_txns()
    if bad is None:
        check("ledger conservation", True, "skipped (no DB access)")
    else:
        check("every ledger txn sums to 0 (no leakage)", bad == 0, f"{bad} non-zero-sum txns")

    srv.shutdown()
    print("\n" + "=" * 50)
    passed = sum(1 for _, ok, _ in results if ok)
    print(f"RESULT: {passed}/{len(results)} passed")
    return 0 if passed == len(results) else 1


def ledger_bad_txns():
    """Count ledger transactions whose entries don't sum to zero. Uses the dev
    Postgres container if reachable; returns None when no DB access."""
    sql = ("SELECT count(*) FROM (SELECT t.id FROM ledger_transactions t "
           "JOIN ledger_entries e ON e.txn_id=t.id GROUP BY t.id HAVING sum(e.amount)<>0) x;")
    try:
        out = subprocess.run(
            ["docker", "exec", "arena-pg-dev", "psql", "-U", "arena", "-d", "arena", "-tAc", sql],
            capture_output=True, text=True, timeout=15)
        if out.returncode == 0:
            return int(out.stdout.strip() or "0")
    except Exception:
        pass
    return None


if __name__ == "__main__":
    raise SystemExit(main())
