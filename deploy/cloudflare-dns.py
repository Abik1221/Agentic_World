#!/usr/bin/env python3
"""Reconcile Cloudflare DNS for the Pyyol domain — idempotent, safe to re-run.

Creates/updates exactly the records the platform needs and removes stale apex/www
A records that point at a different IP. Nothing is hard-coded secret: it reads

    CLOUDFLARE_API_TOKEN   (required) — token with Zone:Read + Zone.DNS:Edit on the zone
    DOMAIN                 (default: pyyol.com)
    SERVER_IP              (required) — the origin server's public IPv4

Managed records (see deploy/nginx/*.conf for the origin port mapping):
    @      A      SERVER_IP   proxied      -> user app        (pyyol.com)
    www    CNAME  @           proxied      -> apex
    api    A      SERVER_IP   DNS-only     -> arena API (long-lived WS/SSE; bypass proxy)
    admin  A      SERVER_IP   proxied      -> admin
    trace  A      SERVER_IP   proxied      -> Pyyol Lens

Usage:
    CLOUDFLARE_API_TOKEN=... SERVER_IP=76.13.48.131 python3 deploy/cloudflare-dns.py [--dry-run]
"""
import json
import os
import sys
import urllib.error
import urllib.request

API = "https://api.cloudflare.com/client/v4"
DRY = "--dry-run" in sys.argv


def die(msg):
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(1)


def cf(path, method="GET", body=None, token=""):
    req = urllib.request.Request(
        API + path,
        data=(json.dumps(body).encode() if body is not None else None),
        method=method,
    )
    req.add_header("Authorization", f"Bearer {token}")
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return json.loads(r.read() or b"{}")
    except urllib.error.HTTPError as e:
        try:
            return json.loads(e.read() or b"{}")
        except Exception:
            return {"success": False, "errors": [{"message": f"HTTP {e.code}"}]}


def ok(resp, what):
    if not resp.get("success"):
        errs = "; ".join(e.get("message", "?") for e in resp.get("errors", []))
        die(f"{what}: {errs}")
    return resp["result"]


def main():
    token = os.environ.get("CLOUDFLARE_API_TOKEN", "").strip()
    domain = os.environ.get("DOMAIN", "pyyol.com").strip()
    ip = os.environ.get("SERVER_IP", "").strip()
    if not token:
        die("CLOUDFLARE_API_TOKEN is required")
    if not ip:
        die("SERVER_IP is required")

    # Fail fast on the common footguns: a not-yet-valid or expired token verifies
    # as 'active' but every real call returns 9109 'Invalid access token'.
    v = cf("/user/tokens/verify", token=token)
    if not v.get("success"):
        die("token verify failed — check the token value")
    res = v.get("result", {})
    if res.get("not_before"):
        die(f"token is not valid until {res['not_before']} (a future Start Date) — "
            "recreate it without a start date")
    if res.get("expires_on"):
        print(f"note: token expires {res['expires_on']}")

    zones = ok(cf(f"/zones?name={domain}", token=token), "list zone")
    if not zones:
        die(f"no zone for {domain} — the token needs Zone:Read on this zone")
    zid = zones[0]["id"]
    print(f"zone {domain} = {zid}")

    def fqdn(name):
        return domain if name == "@" else f"{name}.{domain}"

    desired = [
        {"name": "@", "type": "A", "content": ip, "proxied": True},
        {"name": "www", "type": "CNAME", "content": domain, "proxied": True},
        {"name": "api", "type": "A", "content": ip, "proxied": False},
        {"name": "admin", "type": "A", "content": ip, "proxied": True},
        {"name": "trace", "type": "A", "content": ip, "proxied": True},
    ]

    for d in desired:
        name = fqdn(d["name"])
        existing = ok(cf(f"/zones/{zid}/dns_records?name={name}", token=token), f"list {name}")
        # Remove any record on this name that is NOT the one we want (stale A on a
        # different IP, wrong type, a conflicting CNAME/A, etc.).
        keep = None
        for r in existing:
            same = (r["type"] == d["type"] and r["content"] == d["content"])
            if same and keep is None:
                keep = r
                continue
            print(f"  delete stale {r['type']} {name} -> {r['content']}"
                  f"{' [dry-run]' if DRY else ''}")
            if not DRY:
                ok(cf(f"/zones/{zid}/dns_records/{r['id']}", "DELETE", token=token), "delete")
        payload = {"type": d["type"], "name": name, "content": d["content"],
                   "proxied": d["proxied"], "ttl": 1}
        cloud = "proxied" if d["proxied"] else "DNS-only"
        if keep is None:
            print(f"  create {d['type']} {name} -> {d['content']} ({cloud})"
                  f"{' [dry-run]' if DRY else ''}")
            if not DRY:
                ok(cf(f"/zones/{zid}/dns_records", "POST", payload, token=token), "create")
        elif keep.get("proxied") != d["proxied"]:
            print(f"  update {d['type']} {name} proxied -> {d['proxied']}"
                  f"{' [dry-run]' if DRY else ''}")
            if not DRY:
                ok(cf(f"/zones/{zid}/dns_records/{keep['id']}", "PUT", payload, token=token), "update")
        else:
            print(f"  ok     {d['type']} {name} -> {d['content']} ({cloud})")

    print("done." if not DRY else "dry-run complete (no changes made).")


if __name__ == "__main__":
    main()
