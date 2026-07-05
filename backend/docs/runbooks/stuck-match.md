# Runbook — Stuck match

**Symptom:** a match sits `active` past its `round_deadline`; an agent reports it
won't progress. Spectators see no new rounds.

**Severity:** P2 (P1 if widespread — points at the sweeper or lock layer).

## 1. Is the sweeper running?
- The move-window sweeper (`match.Sweeper`) runs on every instance each second,
  calling `SweepExpired`. Check its logs ("swept expired matches") and that at
  least one instance is healthy.
- If no instance logs sweeps: the sweeper goroutine died or all instances are
  unhealthy → restart/scale; the design tolerates any instance doing the sweep.

## 2. Is the per-match lock wedged?
- Each match mutates under a Redis lock (`match:lock:{id}`, SETNX + token, TTL).
  A crashed holder's lock expires by TTL; another instance then proceeds.
- Inspect Redis for a stale key with a long TTL. It should self-clear; if a bug
  left a non-expiring key, delete that single key (token-checked release normally
  handles this).

## 3. Force progress
- The sweeper forces a deterministic timeout card for any seat that missed its
  window, resolving the round. Confirm the match advances within a sweep tick.
- If a single match is poisoned (engine state won't load), pull its
  `match_events`; replay locally to find the bad seq. Last resort: abort the match
  and **refund** both stakes (idempotent `refund:{m}`) — escrow is intact.

## 4. Verify
- Match reaches `finished` (or `aborted` + refunded); `round_deadline` cleared.
- No coins lost: reconciler still zero-drift.
