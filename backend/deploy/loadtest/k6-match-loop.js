// k6 load harness — Stage 10 capacity validation.
//
//   Simulates the full agent loop (create/join → play to finish) plus a pool of
//   passive spectators, ramping toward the concurrency targets in
//   docs/architecture/concurrency-scaling.md §6:
//     concurrent agents 10k+, live matches 5k+, action p99 < 150ms, settle p95 < 2s.
//
// Run against STAGING only (it creates real matches + moves test coins):
//   BASE_URL=https://staging.agent-arena.example \
//   AGENT_KEYS=sk_arena_a_...,sk_arena_b_... \
//   k6 run deploy/loadtest/k6-match-loop.js
//
// Provide pre-provisioned, funded agent keys via AGENT_KEYS (comma-separated).
// The harness pairs them two at a time. Thresholds FAIL the run if targets miss.

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Trend, Rate } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const KEYS = (__ENV.AGENT_KEYS || '').split(',').filter(Boolean);

const actionLatency = new Trend('action_latency_ms', true);
const settleLatency = new Trend('settle_latency_ms', true);
const errors = new Rate('loop_errors');

export const options = {
  scenarios: {
    // Agents driving matches: ramp to a sustained arrival rate.
    agents: {
      executor: 'ramping-vus',
      exec: 'agentLoop',
      startVUs: 0,
      stages: [
        { duration: '2m', target: 200 },   // warm up
        { duration: '5m', target: 1000 },  // climb
        { duration: '10m', target: 1000 }, // sustain (soak slice)
        { duration: '2m', target: 0 },     // drain
      ],
    },
    // Passive spectators (SSE); in prod these are CDN-fronted.
    spectators: {
      executor: 'constant-vus',
      exec: 'spectate',
      vus: 500,
      duration: '19m',
    },
  },
  thresholds: {
    action_latency_ms: ['p(99)<150'],
    settle_latency_ms: ['p(95)<2000'],
    loop_errors: ['rate<0.01'],
    http_req_failed: ['rate<0.01'],
  },
};

function authHeaders(key) {
  return { headers: { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' } };
}

// agentLoop: one VU = one agent that pairs with another and plays a match.
export function agentLoop() {
  if (KEYS.length < 2) {
    errors.add(1);
    sleep(1);
    return;
  }
  const a = KEYS[(__VU * 2) % KEYS.length];
  const b = KEYS[(__VU * 2 + 1) % KEYS.length];

  // A opens, B joins.
  const created = http.post(`${BASE}/v1/lobby/create`, JSON.stringify({ bid: 10 }), authHeaders(a));
  if (!check(created, { 'created 201': (r) => r.status === 201 })) { errors.add(1); return; }
  const matchId = created.json('match_id');

  const joined = http.post(`${BASE}/v1/lobby/join`, JSON.stringify({ match_id: matchId }), authHeaders(b));
  if (!check(joined, { 'joined 200': (r) => r.status === 200 })) { errors.add(1); return; }

  // Play each agent's lowest legal card until the match finishes.
  for (let round = 1; round <= 13; round++) {
    for (const key of [a, b]) {
      const stateRes = http.get(`${BASE}/v1/match/${matchId}/state`, authHeaders(key));
      const st = stateRes.json();
      if (st.status === 'finished') { return; }
      if (st.your_turn && st.you && st.you.hand && st.you.hand.length) {
        const t0 = Date.now();
        const act = http.post(`${BASE}/v1/match/${matchId}/action`,
          JSON.stringify({ round: st.round, card: st.you.hand[0] }), authHeaders(key));
        actionLatency.add(Date.now() - t0);
        check(act, { 'action 200': (r) => r.status === 200 }) || errors.add(1);
        if (act.json('status') === 'finished') {
          settleLatency.add(Date.now() - t0);
          return;
        }
      }
    }
  }
}

// spectate: subscribe to the live list and one match's stream briefly.
export function spectate() {
  const live = http.get(`${BASE}/v1/matches/live`);
  check(live, { 'live 200': (r) => r.status === 200 });
  const matches = (live.json('matches') || []);
  if (matches.length) {
    http.get(`${BASE}/v1/match/${matches[0].match_id}/watch?timeout=2`);
  }
  sleep(2);
}
