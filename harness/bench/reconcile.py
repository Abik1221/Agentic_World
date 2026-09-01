#!/usr/bin/env python3
"""Join the local ledgers against the database and report what did not survive the trip.

    python3 reconcile.py --state ./state --run 20260816-120000 \
        --dsn postgres://pyyol:pyyol@localhost:5432/pyyol_lab

# Why this exists

The gateway's `agent_model_calls` row is the authoritative record and the only thing the
published board may be computed from. That is exactly why it cannot be the only record: a
single source of truth has nothing to be checked against, and this platform has already been
bitten once by trusting it. A harness run went out where every OpenRouter call returned 429
or 401 with zero tokens, and every one of those rows was written `bound = true` — binding is
decided from the turn proof BEFORE the upstream is called, so nothing on the row said the
provider had never answered. Two models were given a win rate having never emitted a token.

So each agent process writes what it OBSERVED, locally, and this script joins the two. It
answers four questions, and all four answers should be zero:

  MISSING   we recorded a call the database has no row for — data lost between us and the DB
  ORPHANED  the database has a row we have no record of — something else wrote as us
  UNANSWERED we started a call that never came back — a timeout, a kill, a crash
  MISMATCH  both sides have the row and disagree on the token counts

It also reports the two numbers that decide whether the run is publishable at all: binding
coverage per seat (the board excludes a seat below 0.9) and the share of decisions that came
from the model rather than from the fallback heuristic. A run can be perfectly reconciled and
still be worthless if half its decisions were fallbacks, so both are printed together.
"""

from __future__ import annotations

import argparse
import json
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any


def load_ledger(state: Path, run: str) -> list[dict[str, Any]]:
    """Every record from every agent process in one run.

    A truncated final line is skipped rather than fatal: the ledger is fsynced per line, so
    a partial line means the process was killed mid-write and the loss is exactly that one
    record — which the UNANSWERED count will surface anyway.
    """
    root = state / "ledger" / run
    if not root.is_dir():
        raise SystemExit(f"no ledger directory at {root}")
    out: list[dict[str, Any]] = []
    for f in sorted(root.glob("*.jsonl")):
        for line in f.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            rec["_file"] = f.name
            out.append(rec)
    return out


def fetch_db(dsn: str, match_ids: list[str]) -> dict[str, Any]:
    """The authoritative side: model calls, decisions and bound decisions for these matches."""
    try:
        import psycopg
    except ImportError:
        raise SystemExit("pip install 'psycopg[binary]' to reconcile against the database")

    if not match_ids:
        return {"calls": [], "decisions": [], "bound": []}

    with psycopg.connect(dsn) as conn, conn.cursor() as cur:
        cur.execute(
            """
            SELECT match_id, agent_id, turn, model, provider, status, bound,
                   prompt_tokens, completion_tokens, upstream_host
              FROM agent_model_calls
             WHERE match_id = ANY(%s)
            """,
            (match_ids,),
        )
        calls = [
            dict(
                zip(
                    ("match_id", "agent_id", "turn", "model", "provider", "status", "bound",
                     "prompt_tokens", "completion_tokens", "upstream_host"),
                    r,
                )
            )
            for r in cur.fetchall()
        ]
        cur.execute(
            "SELECT match_id, agent_id, COUNT(*) FROM agent_match_decisions "
            "WHERE match_id = ANY(%s) GROUP BY 1,2",
            (match_ids,),
        )
        decisions = [{"match_id": a, "agent_id": b, "n": c} for a, b, c in cur.fetchall()]
        cur.execute(
            "SELECT match_id, agent_id, COUNT(DISTINCT round) FROM agent_match_bound_decisions "
            "WHERE match_id = ANY(%s) GROUP BY 1,2",
            (match_ids,),
        )
        bound = [{"match_id": a, "agent_id": b, "n": c} for a, b, c in cur.fetchall()]
    return {"calls": calls, "decisions": decisions, "bound": bound}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--state", default="./state", type=Path)
    ap.add_argument("--run", required=True)
    ap.add_argument("--dsn", default="", help="omit to report ledger-side findings only")
    args = ap.parse_args()

    recs = load_ledger(args.state, args.run)
    starts = {(r["match_id"], r["turn"], r["seat"]) for r in recs if r["kind"] == "call_start"}
    ends = {(r["match_id"], r["turn"], r["seat"]) for r in recs if r["kind"] == "call_end"}
    call_ends = [r for r in recs if r["kind"] == "call_end"]
    decisions = [r for r in recs if r["kind"] == "decision"]

    print(f"=== run {args.run} — local ledger ===")
    print(f"  agent processes : {len({r['_file'] for r in recs})}")
    print(f"  matches seen    : {len({r.get('match_id') for r in recs if r.get('match_id')})}")
    print(f"  calls started   : {len(starts)}")
    print(f"  calls completed : {len(ends)}")

    unanswered = starts - ends
    if unanswered:
        print(f"  UNANSWERED      : {len(unanswered)}  <-- calls that left and never returned")
        for k in sorted(unanswered)[:10]:
            print(f"      match={k[0]} turn={k[1]} seat={k[2]}")
    else:
        print("  UNANSWERED      : 0")

    # Decision provenance. The headline honesty number: a model credited for a win it did
    # not make is the failure mode this whole harness is built to avoid.
    by_source: dict[str, int] = defaultdict(int)
    for d in decisions:
        by_source[d.get("source", "?")] += 1
    total = sum(by_source.values()) or 1
    print("\n=== decision provenance ===")
    for src in ("model", "repair", "fallback"):
        n = by_source.get(src, 0)
        print(f"  {src:<9}: {n:5d}  ({100.0 * n / total:5.1f}%)")
    if by_source.get("fallback", 0) / total > 0.10:
        print("  WARNING: >10% of decisions were heuristic fallbacks. This run measures the")
        print("           fallback as much as the model; treat the win rate as contaminated.")

    # Substitution check. On an honest run these are always equal; a disagreement means
    # either an agent bug or a drift between the SDK's canonicaliser and the gateway's, and
    # the second would reject honest moves in production.
    mism = [
        r for r in call_ends
        if r.get("bound_move") and r.get("submitted_move")
        and r["bound_move"] != r["submitted_move"]
    ]
    print(f"\n  bound != submitted : {len(mism)}"
          + ("  <-- investigate before publishing" if mism else "  (as expected)"))
    for r in mism[:5]:
        print(f"      match={r['match_id']} turn={r['turn']} "
              f"bound={r['bound_move']} submitted={r['submitted_move']}")

    # Cost, from what we observed. Compared against the invoice, not a substitute for it.
    spend = sum(float(r.get("cost_usd") or 0) for r in call_ends)
    ptok = sum(int(r.get("prompt_tokens") or 0) for r in call_ends)
    ctok = sum(int(r.get("completion_tokens") or 0) for r in call_ends)
    rtok = sum(int(r.get("reasoning_tokens") or 0) for r in call_ends)
    cached = sum(int(r.get("cached_read") or 0) for r in call_ends)
    print("\n=== observed cost ===")
    print(f"  prompt tokens   : {ptok:,}   (cached reads: {cached:,} = "
          f"{100.0 * cached / (ptok or 1):.1f}%)")
    print(f"  completion      : {ctok:,}   (of which reasoning: {rtok:,})")
    print(f"  projected spend : ${spend:.4f}")
    if ptok and cached / ptok < 0.30:
        print("  NOTE: cache hit rate is low. The system prompt should be a cached read on")
        print("        every call after the first of a match; if it is not, the run is paying")
        print("        full price for the rules on every decision.")

    if not args.dsn:
        print("\n(no --dsn given: database side not checked)")
        return 0

    match_ids = sorted({r["match_id"] for r in recs if r.get("match_id")})
    db = fetch_db(args.dsn, match_ids)
    db_calls = db["calls"]

    print("\n=== database ===")
    print(f"  agent_model_calls rows : {len(db_calls)}")
    ours = {(r["match_id"], r["turn"]) for r in call_ends}
    theirs = {(r["match_id"], int(r["turn"] or 0)) for r in db_calls}
    missing = ours - theirs
    orphaned = theirs - ours
    print(f"  MISSING  (we have, DB does not): {len(missing)}")
    for k in sorted(missing)[:10]:
        print(f"      match={k[0]} turn={k[1]}")
    print(f"  ORPHANED (DB has, we do not)  : {len(orphaned)}")
    for k in sorted(orphaned)[:10]:
        print(f"      match={k[0]} turn={k[1]}")

    # The specific bug that motivated all of this: a row marked bound whose provider never
    # answered. The attribution query now filters these out, so seeing any here means the
    # run's coverage will be lower than the row count suggests.
    zero_token_bound = [
        c for c in db_calls
        if c["bound"] and not (c["prompt_tokens"] or 0) and not (c["completion_tokens"] or 0)
    ]
    if zero_token_bound:
        print(f"\n  {len(zero_token_bound)} rows are bound=true with ZERO tokens.")
        print("  These are calls the provider never answered. The model board's attribution")
        print("  filter (status 2xx) excludes them, so expect coverage below the raw count.")
        statuses: dict[int, int] = defaultdict(int)
        for c in zero_token_bound:
            statuses[int(c["status"] or 0)] += 1
        print(f"  status breakdown: {dict(sorted(statuses.items()))}")

    # Coverage per seat: bound decisions over logged decisions. Below 0.9 the board drops
    # the seat, and a dropped seat is a paid-for match that publishes nothing.
    logged = {(d["match_id"], d["agent_id"]): d["n"] for d in db["decisions"]}
    boundc = {(b["match_id"], b["agent_id"]): b["n"] for b in db["bound"]}
    print("\n=== binding coverage (board threshold 0.90) ===")
    below = 0
    for key, n in sorted(logged.items()):
        b = boundc.get(key, 0)
        cov = b / n if n else 0.0
        if cov < 0.90:
            below += 1
            if below <= 10:
                print(f"  match={key[0]} agent={key[1]}: {b}/{n} = {cov:.2f}  BELOW THRESHOLD")
    print(f"  seats below threshold: {below} of {len(logged)}")

    problems = len(missing) + len(orphaned) + len(unanswered) + len(mism)
    print("\n=== verdict ===")
    if problems == 0 and below == 0:
        print("  clean: every call we made is in the database, none is bound to a move we")
        print("  did not submit, and every seat clears the coverage threshold.")
        return 0
    print(f"  {problems} data-integrity finding(s), {below} seat(s) below coverage.")
    print("  Do NOT publish until these are explained.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
