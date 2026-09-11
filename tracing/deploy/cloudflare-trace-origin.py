#!/usr/bin/env python3
"""Keep Cloudflare origin for trace.<domain> on :443 (same as admin/pyyol.com).

A previous deploy pointed this hostname at origin :3110. That port is dropped
by the Hostinger panel firewall (only 22/80/443 answer from the internet), so
the override either never applied (Mega Hub on :443) or would 522.

The named nginx vhost is the working path — identical to admin.pyyol.com.
This script removes (or refuses to add) an origin-port override when
TRACE_PUBLIC_PORT is 80 or 443. Setting TRACE_PUBLIC_PORT=3110 restores the
old override for operators who have opened that port.

Existing origin-phase rules are preserved; this never replaces the whole set
with only our rule. Safe to re-run (idempotent).

Env:
    CLOUDFLARE_API_TOKEN   required
    DOMAIN                 default pyyol.com
    TRACE_PUBLIC_PORT      default 443 (no override). 3110 = send to Eye's public port
    CLOUDFLARE_ZONE_ID     optional (looked up from DOMAIN if unset)

    --dry-run              print the intended change, do not PUT
"""
import json
import os
import sys
import urllib.error
import urllib.request

API = "https://api.cloudflare.com/client/v4"
DRY = "--dry-run" in sys.argv
RULE_DESC = "pyyol-eye: origin port (do not send trace hostname to Mega Hub :80)"
PHASE = "http_request_origin"


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
            return json.loads(r.read() or b"{}"), r.status
    except urllib.error.HTTPError as e:
        try:
            payload = json.loads(e.read() or b"{}")
        except Exception:
            payload = {"success": False, "errors": [{"message": f"HTTP {e.code}"}]}
        return payload, e.code


def ok(resp, what):
    if not resp.get("success"):
        errs = "; ".join(e.get("message", "?") for e in resp.get("errors", []))
        die(f"{what}: {errs}")
    return resp.get("result") or {}


def main():
    token = os.environ.get("CLOUDFLARE_API_TOKEN", "").strip()
    domain = os.environ.get("DOMAIN", "pyyol.com").strip()
    port = int(os.environ.get("TRACE_PUBLIC_PORT", "443"))
    zone_id = os.environ.get("CLOUDFLARE_ZONE_ID", "").strip()
    if not token:
        print("CLOUDFLARE_API_TOKEN unset — skip origin-rule reconcile.")
        return
    host = f"trace.{domain}"
    expression = f'(http.host eq "{host}")'
    # 80/443 is Cloudflare's default origin. An override to 3110 is only useful
    # after the Hostinger panel firewall allows that port.
    drop_override = port in (80, 443)
    our_rule = {
        "description": RULE_DESC,
        "expression": expression,
        "action": "route",
        "action_parameters": {"origin": {"port": port}},
        "enabled": True,
    }

    if not zone_id:
        zones, _ = cf(f"/zones?name={domain}", token=token)
        zlist = ok(zones, "list zone")
        if not zlist:
            die(f"no zone for {domain}")
        zone_id = zlist[0]["id"]
    print(f"zone {domain} = {zone_id}; origin {host} → :{port}")

    entry, status = cf(
        f"/zones/{zone_id}/rulesets/phases/{PHASE}/entrypoint",
        token=token,
    )
    rules = []
    if status == 404 or not entry.get("success"):
        if drop_override:
            print("no origin-phase ruleset; Cloudflare already uses origin :443.")
            return
        print("no origin-phase ruleset yet; will create one with the Eye rule only")
        rules = [our_rule]
    else:
        current = ok(entry, "get origin ruleset")
        rules = list(current.get("rules") or [])
        replaced = False
        kept = []
        for r in rules:
            same = (
                r.get("description") == RULE_DESC
                or r.get("expression") == expression
                or host in (r.get("expression") or "")
            )
            if same and drop_override:
                print(f"  dropping origin-port override for {host} (use default :443)")
                replaced = True
                continue
            if same:
                merged = {**r, **our_rule}
                if r.get("id"):
                    merged["id"] = r["id"]
                kept.append(merged)
                replaced = True
                continue
            kept.append(r)
        rules = kept
        if not replaced and not drop_override:
            rules.append(our_rule)
        if drop_override and not replaced:
            print(f"no origin-port override for {host}; Cloudflare already uses :443.")
            return

    print(f"  {'dry-run ' if DRY else ''}PUT {len(rules)} origin-phase rule(s)")
    for r in rules:
        print(f"    - {r.get('description') or r.get('expression')}")
    if DRY:
        print("dry-run complete (no changes made).")
        return
    updated, _ = cf(
        f"/zones/{zone_id}/rulesets/phases/{PHASE}/entrypoint",
        method="PUT",
        body={"rules": rules},
        token=token,
    )
    ok(updated, "upsert origin ruleset")
    if drop_override:
        print(f"done. Cloudflare will send {host} to origin :443 (named vhost, same as admin).")
    else:
        print(f"done. Cloudflare will send {host} to origin:{port}.")


if __name__ == "__main__":
    main()
