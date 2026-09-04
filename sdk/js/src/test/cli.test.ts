import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { apiGet, apiPost, apiRequest, connectionToken, main, type Args } from "../cli.js";
import type { Credentials } from "../credentials.js";

/**
 * Drive `main()` with a stubbed global fetch, captured stdout/stderr, and an
 * isolated PYYOL_HOME so nothing touches the real network or the user's creds.
 * Everything is saved and restored so tests don't leak into each other.
 */
async function run(
  argv: string[],
  opts: { fetch?: typeof fetch; home?: string } = {},
): Promise<{ code: number; out: string; err: string }> {
  const origLog = console.log;
  const origErr = console.error;
  const origFetch = globalThis.fetch;
  const origHome = process.env.PYYOL_HOME;
  const origApi = process.env.PYYOL_API;
  const origTelemetry = process.env.PYYOL_NO_TELEMETRY;
  process.env.PYYOL_NO_TELEMETRY = "1"; // no adoption ping during tests (no real network)
  const out: string[] = [];
  const err: string[] = [];
  console.log = (...a: unknown[]) => out.push(a.join(" "));
  console.error = (...a: unknown[]) => err.push(a.join(" "));
  delete process.env.PYYOL_API; // keep httpBase deterministic
  if (opts.home) process.env.PYYOL_HOME = opts.home;
  if (opts.fetch) globalThis.fetch = opts.fetch;
  try {
    const code = await main(argv);
    return { code, out: out.join("\n"), err: err.join("\n") };
  } finally {
    console.log = origLog;
    console.error = origErr;
    globalThis.fetch = origFetch;
    if (origHome === undefined) delete process.env.PYYOL_HOME;
    else process.env.PYYOL_HOME = origHome;
    if (origApi === undefined) delete process.env.PYYOL_API;
    else process.env.PYYOL_API = origApi;
    if (origTelemetry === undefined) delete process.env.PYYOL_NO_TELEMETRY;
    else process.env.PYYOL_NO_TELEMETRY = origTelemetry;
  }
}

function seedCreds(home: string, extra: Record<string, unknown> = {}): void {
  mkdirSync(home, { recursive: true });
  writeFileSync(
    join(home, "credentials.json"),
    JSON.stringify({
      url: "http://localhost:9999",
      connectUrl: "ws://localhost:9999/connect",
      agentId: "ag_test",
      accessToken: "tok",
      refreshToken: "",
      ...extra,
    }),
  );
}

const json = (obj: unknown, status = 200) => new Response(JSON.stringify(obj), { status });

const rateLimited = () =>
  new Response(JSON.stringify({ error: "rate_limited" }), {
    status: 429,
    headers: { "Retry-After": "0" }, // 0s → no real sleep, keeps the test fast
  });

// ── 429 / Retry-After retry (HTTP helpers) ────────────────────────────────────

test("apiGet retries a 429 (Retry-After) then returns the 200", async () => {
  const origFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = (async () => (++calls === 1 ? rateLimited() : json({ ok: true }))) as typeof fetch;
  try {
    const [status, body] = await apiGet("http://localhost:9999/v1/thing");
    assert.equal(calls, 2); // one retry after the 429
    assert.equal(status, 200);
    assert.deepEqual(body, { ok: true });
  } finally {
    globalThis.fetch = origFetch;
  }
});

test("apiPost retries a 429 then returns the 200", async () => {
  const origFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = (async () => (++calls === 1 ? rateLimited() : json({ done: true }))) as typeof fetch;
  try {
    const [status, body] = await apiPost("http://localhost:9999/v1/thing", "tok", { a: 1 });
    assert.equal(calls, 2);
    assert.equal(status, 200);
    assert.deepEqual(body, { done: true });
  } finally {
    globalThis.fetch = origFetch;
  }
});

test("apiRequest stops after the retry cap and returns the final 429", async () => {
  const origFetch = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = (async () => (calls++, rateLimited())) as typeof fetch;
  try {
    const [status, body] = await apiRequest("PUT", "http://localhost:9999/v1/x", "tok", {});
    assert.equal(calls, 3); // 1 initial + 2 retries, then give up
    assert.equal(status, 429);
    assert.deepEqual(body, { error: "rate_limited" });
  } finally {
    globalThis.fetch = origFetch;
  }
});

// ── connectionToken (the agent-key-vs-JWT connection credential) ──────────────

const noArgs: Args = { positionals: [], flags: {} };
const withToken = (t: string): Args => ({ positionals: [], flags: { token: t } });
const credsOf = (extra: Partial<Credentials>): Credentials => ({
  url: "http://localhost:9999",
  connectUrl: "ws://localhost:9999/connect",
  agentId: "ag_test",
  accessToken: "",
  refreshToken: "",
  apiKey: "",
  ...extra,
});

test("connectionToken prefers the long-lived agent key over the dashboard JWT", () => {
  const c = credsOf({ accessToken: "jwt-tok", apiKey: "sk_arena_a_b" });
  const r = connectionToken(noArgs, c);
  assert.equal(r.token, "sk_arena_a_b");
  assert.equal(r.usingAgentKey, true);
});

test("connectionToken falls back to the dashboard JWT when there is no agent key", () => {
  const c = credsOf({ accessToken: "jwt-tok" });
  const r = connectionToken(noArgs, c);
  assert.equal(r.token, "jwt-tok");
  assert.equal(r.usingAgentKey, false);
});

test("connectionToken: an explicit sk_arena_ token is treated as the agent key", () => {
  const r = connectionToken(withToken("sk_arena_x_y"), credsOf({ accessToken: "jwt-tok", apiKey: "sk_arena_a_b" }));
  assert.equal(r.token, "sk_arena_x_y"); // explicit --token wins over stored creds
  assert.equal(r.usingAgentKey, true);
});

test("connectionToken: an explicit non-key token is NOT the agent key", () => {
  const r = connectionToken(withToken("some-jwt"), credsOf({ apiKey: "sk_arena_a_b" }));
  assert.equal(r.token, "some-jwt");
  assert.equal(r.usingAgentKey, false);
});

// ── status ──────────────────────────────────────────────────────────────────

test("status prints Online + details when the agent is up", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-h-"));
  seedCreds(home);
  let calledUrl = "";
  const { code, out } = await run(["status", "--api", "http://localhost:9999", "--agent", "ag_test"], {
    home,
    fetch: async (u) => {
      calledUrl = String(u);
      return json({ online: true, sdk_version: "1.2.3", games: ["goofspiel"], last_seen: "2026-07-22" });
    },
  });
  assert.equal(code, 0);
  assert.match(calledUrl, /\/v1\/agent\/status\?agent_id=ag_test$/);
  assert.match(out, /Online/);
  assert.match(out, /1\.2\.3/);
  assert.match(out, /goofspiel/);
});

test("status prints Offline and omits details when the agent is down", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-h-"));
  seedCreds(home);
  const { code, out } = await run(["status", "--api", "http://localhost:9999", "--agent", "ag_test"], {
    home,
    fetch: async () => json({ online: false }),
  });
  assert.equal(code, 0);
  assert.match(out, /Offline/);
  assert.doesNotMatch(out, /SDK/);
});

test("status without login is a usage error", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-h-"));
  const { code, err } = await run(["status"], { home });
  assert.equal(code, 2);
  assert.match(err, /not logged in/);
});

// ── autoplay ──────────────────────────────────────────────────────────────────

test("autoplay on PUTs enabled=true with resolved mode/bid/games", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-h-"));
  seedCreds(home);
  let captured: { url: string; method: string; body: Record<string, unknown> } | undefined;
  const { code, out } = await run(
    ["autoplay", "on", "--api", "http://localhost:9999", "--mode", "sandbox", "--games", "goofspiel,mafia", "--bid", "5"],
    {
      home,
      fetch: async (u, init) => {
        captured = { url: String(u), method: String(init?.method), body: JSON.parse(String(init?.body)) };
        return json({ ok: true });
      },
    },
  );
  assert.equal(code, 0);
  assert.ok(captured);
  assert.equal(captured!.method, "PUT");
  assert.match(captured!.url, /\/v1\/agent\/autoplay$/);
  assert.deepEqual(captured!.body, { enabled: true, mode: "sandbox", bid: 5, games: ["goofspiel", "mafia"] });
  assert.match(out, /auto-play ON/);
});

test("autoplay off PUTs enabled=false", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-h-"));
  seedCreds(home);
  let body: Record<string, unknown> | null = null;
  const { code, out } = await run(["autoplay", "off", "--api", "http://localhost:9999"], {
    home,
    fetch: async (_u, init) => {
      body = JSON.parse(String(init?.body));
      return json({});
    },
  });
  assert.equal(code, 0);
  assert.equal(body!.enabled, false);
  assert.match(out, /auto-play OFF/);
});

test("autoplay status GETs and explains why it isn't playing", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-h-"));
  seedCreds(home);
  let method: string | undefined;
  const { code, out } = await run(["autoplay", "status", "--api", "http://localhost:9999"], {
    home,
    fetch: async (_u, init) => {
      method = String(init?.method ?? "GET");
      return json({
        enabled: true,
        mode: "ranked",
        last_status: "blocked",
        last_status_reason: "This agent is not currently reachable",
      });
    },
  });
  assert.equal(code, 0);
  assert.equal(method, "GET");
  assert.match(out, /auto-play is ON/);
  assert.match(out, /not playing — This agent is not currently reachable/);
  assert.match(out, /resumes automatically/);
});

test("autoplay surfaces a non-2xx failure", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-h-"));
  seedCreds(home);
  const { code, err } = await run(["autoplay", "on", "--api", "http://localhost:9999"], {
    home,
    fetch: async () => json({ error: "nope" }, 500),
  });
  assert.equal(code, 1);
  assert.match(err, /failed \(status 500\)/);
});

// ── logs ────────────────────────────────────────────────────────────────────

test("logs tails the last N lines of the run log", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-h-"));
  mkdirSync(join(home, "logs"), { recursive: true });
  const lines = Array.from({ length: 10 }, (_, i) => `line ${i}`);
  writeFileSync(join(home, "logs", "agent.log"), lines.join("\n") + "\n");
  const { code, out } = await run(["logs", "--n", "3"], { home });
  assert.equal(code, 0);
  assert.deepEqual(out.split("\n"), ["line 7", "line 8", "line 9"]);
});

test("logs reports 'no logs yet' when the file is missing", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-h-"));
  const { code, out } = await run(["logs"], { home });
  assert.equal(code, 0);
  assert.match(out, /no logs yet/);
});

// ── simulate ──────────────────────────────────────────────────────────────────

test("simulate runs a local Goofspiel match against the configured agent", async () => {
  const dir = mkdtempSync(join(tmpdir(), "pyyol-p-"));
  // Import the built Agent by absolute path so the temp project needn't resolve
  // the `pyyol` package; asAgent is duck-typed so a different module copy is fine.
  const serverUrl = new URL("../server.js", import.meta.url).href;
  writeFileSync(
    join(dir, "agent.mjs"),
    `import { Agent } from ${JSON.stringify(serverUrl)};\n` +
      `const a = new Agent({ supportedGames: ["goofspiel"], name: "t" });\n` +
      `a.onTurn("goofspiel", (v) => ({ round: v.round, card: Math.min(...v.legal_actions) }));\n` +
      `export const agent = a;\n`,
  );
  writeFileSync(
    join(dir, "pyyol.toml"),
    `name = "t"\nlanguage = "javascript"\narena = "goofspiel"\nmode = "sandbox"\nentry = "agent.mjs:agent"\n`,
  );
  const cwd = process.cwd();
  process.chdir(dir);
  try {
    const { code, out } = await run(["simulate", "--rounds", "5", "--seed", "1"]);
    assert.equal(code, 0);
    assert.match(out, /simulate goofspiel \(5 rounds\)/);
    assert.match(out, /winner=(agent|baseline|tie)/);
  } finally {
    process.chdir(cwd);
  }
});

test("simulate rejects a non-goofspiel game and points to pyyol dev", async () => {
  const { code, err } = await run(["simulate", "--game", "mafia"]);
  assert.equal(code, 2);
  assert.match(err, /goofspiel only/);
  assert.match(err, /pyyol dev/); // route mafia/monopoly devs to the working loop
});

// ── validate ──────────────────────────────────────────────────────────────────

test("validate reports PASS against a fully compliant endpoint", async () => {
  const url = "http://localhost:9099/turn";
  const { code, out } = await run(["validate", "--url", url, "--game", "goofspiel"], {
    fetch: async (u) => {
      const p = new URL(String(u)).pathname;
      if (p.endsWith("/health")) return json({ status: "healthy" });
      if (p.endsWith("/handshake")) return json({ accepted: true, supportedGames: ["goofspiel"] });
      if (p.endsWith("/turn")) return json({ round: 0, card: 1 });
      return json({}); // initialize / event / game-end
    },
  });
  assert.equal(code, 0);
  assert.match(out, /PASS/);
  assert.match(out, /✓ health/);
});

test("validate reports FAIL when the endpoint misbehaves", async () => {
  const url = "http://localhost:9099/turn";
  const { code, out } = await run(["validate", "--url", url], {
    fetch: async (u) => {
      const p = new URL(String(u)).pathname;
      if (p.endsWith("/health")) return json({ status: "down" }, 500);
      if (p.endsWith("/turn")) return json({}); // no legal card
      return json({});
    },
  });
  assert.equal(code, 1);
  assert.match(out, /FAIL/);
});

test("validate signs handshake/turn when a secret is given", async () => {
  const url = "http://localhost:9099/turn";
  const signed: string[] = [];
  const { code } = await run(["validate", "--url", url, "--secret", "S"], {
    fetch: async (u, init) => {
      const p = new URL(String(u)).pathname;
      const headers = new Headers(init?.headers as HeadersInit);
      if (headers.get("x-arena-signature")) signed.push(p);
      if (p.endsWith("/health")) return json({ status: "healthy" });
      if (p.endsWith("/handshake")) return json({ accepted: true, supportedGames: ["goofspiel"] });
      if (p.endsWith("/turn")) return json({ round: 0, card: 1 });
      return json({});
    },
  });
  assert.equal(code, 0);
  // health is unsigned (GET, no secret); the POSTs carry a signature.
  assert.ok(signed.includes("/handshake"));
  assert.ok(signed.includes("/turn"));
  assert.ok(!signed.includes("/health"));
});

// ── wallet / queue (parity with the Python CLI) ───────────────────────────────

test("wallet prints the treasury balance + per-agent wallets", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-wallet-"));
  seedCreds(home);
  const wallet = {
    coin_cents: 1,
    available_balance: 1500,
    locked_balance: 200,
    agents: [{ name: "atlas", balance: 900, withdrawable: 300 }],
  };
  const { code, out } = await run(["wallet"], { home, fetch: (async () => json(wallet)) as unknown as typeof fetch });
  assert.equal(code, 0);
  assert.match(out, /Available\s+1,500 coins/);
  assert.match(out, /atlas/);
  assert.match(out, /withdrawable 300/);
});

test("wallet without login is a usage error", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-wallet2-"));
  mkdirSync(home, { recursive: true }); // no credentials.json
  const { code, err } = await run(["wallet"], { home });
  assert.equal(code, 2);
  assert.match(err, /not logged in/);
});

test("queue --list shows the stake tiers (positional game)", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-queue-"));
  seedCreds(home);
  const tiers = { tiers: [{ key: "low", coins: 100, label: "Low" }, { key: "mid", coins: 500, label: "Mid" }] };
  const { code, out } = await run(["queue", "goofspiel", "--list"], {
    home,
    fetch: (async () => json(tiers)) as unknown as typeof fetch,
  });
  assert.equal(code, 0);
  assert.match(out, /goofspiel stake tiers/);
  assert.match(out, /low\s+100 coins/);
});

test("queue without a game is a usage error", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-queue2-"));
  seedCreds(home);
  const { code, err } = await run(["queue"], { home });
  assert.equal(code, 2);
  assert.match(err, /usage: pyyol queue <game>/);
});

test("room create posts the stake and prints the shareable id", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-room-"));
  seedCreds(home);
  let posted: any = null;
  let url = "";
  const fetchImpl = (async (u: string, init?: RequestInit) => {
    url = String(u);
    posted = JSON.parse(String(init?.body ?? "{}"));
    return json({ room_id: "mt_room1", match_id: "mt_room1", bid: 500 }, 201);
  }) as unknown as typeof fetch;
  const { code, out } = await run(["room", "create", "--tier", "mid"], { home, fetch: fetchImpl });
  assert.equal(code, 0);
  assert.match(url, /\/v1\/room\/create$/);
  assert.deepEqual(posted, { tier: "mid" });
  // The id must appear alone on its line — the next thing anyone does is select it to paste.
  assert.ok(out.split("\n").some((l) => l.trim() === "mt_room1"), out);
  assert.match(out, /pyyol room join mt_room1/);
});

test("room create refuses to open a FREE table", async () => {
  // A room is staked on both sides. Defaulting to zero would quietly hand someone an
  // unstaked table while the command's whole purpose is a staked one.
  const home = mkdtempSync(join(tmpdir(), "pyyol-room2-"));
  seedCreds(home);
  const { code, err } = await run(["room", "create"], { home });
  assert.equal(code, 2);
  assert.match(err, /a room is staked/);
});

test("room join needs an id", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-room3-"));
  seedCreds(home);
  const { code, err } = await run(["room", "join"], { home });
  assert.equal(code, 2);
  assert.match(err, /which room\?/);
});

test("room join posts the match id", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-room4-"));
  seedCreds(home);
  let posted: any = null;
  let url = "";
  const fetchImpl = (async (u: string, init?: RequestInit) => {
    url = String(u);
    posted = JSON.parse(String(init?.body ?? "{}"));
    return json({ ok: true });
  }) as unknown as typeof fetch;
  const { code, out } = await run(["room", "join", "mt_room1"], { home, fetch: fetchImpl });
  assert.equal(code, 0);
  assert.match(url, /\/v1\/lobby\/join$/);
  assert.deepEqual(posted, { match_id: "mt_room1" });
  assert.match(out, /joined room mt_room1/);
});

test("room reports the arena's refusal in terms a developer can act on", async () => {
  // same_owner is the FIRST failure most people hit: they create a room and then try to
  // join it themselves. The raw JSON says what was refused and never what to do about it.
  const home = mkdtempSync(join(tmpdir(), "pyyol-room5-"));
  seedCreds(home);
  const fetchImpl = (async () => json({ code: "same_owner" }, 409)) as unknown as typeof fetch;
  const { code, err } = await run(["room", "join", "mt_room1"], { home, fetch: fetchImpl });
  assert.equal(code, 1);
  assert.match(err, /your own room/);
});

test("room rejects an unknown action", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-room6-"));
  seedCreds(home);
  const { code, err } = await run(["room", "destroy"], { home });
  assert.equal(code, 2);
  assert.match(err, /usage: pyyol room create/);
});

test("queue posts the tier and reports the match", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-queue3-"));
  seedCreds(home);
  let posted: any = null;
  const fetchImpl = (async (_url: string, init?: RequestInit) => {
    posted = JSON.parse(String(init?.body ?? "{}"));
    return json({ match_id: "mt_9f3" }, 202);
  }) as unknown as typeof fetch;
  const { code, out } = await run(["queue", "goofspiel", "--tier", "mid"], { home, fetch: fetchImpl });
  assert.equal(code, 0);
  assert.deepEqual(posted, { game: "goofspiel", tier: "mid" });
  assert.match(out, /queued for goofspiel \(tier mid\)/);
  assert.match(out, /mt_9f3/);
});

// ── Which credential `queue` and `room` send ────────────────────────────────
//
// /v1/queue, /v1/room/create and /v1/lobby/join are all registered server-side with
// RequireScope(ScopeAgent), so they need the AGENT key. Both commands sent the dashboard
// session token instead, and every attempt came back:
//
//   403 forbidden_scope: This credential is not allowed to access this resource
//
// For every developer, every time — on the command the scaffold prints as THE way to play
// ranked. connectionToken was already tested and already correct; these commands simply
// did not call it.
//
// They also read stored credentials BEFORE the explicit --token flag, so a caller passing
// a credential was ignored whenever anything happened to be logged in on the machine.

function authHeadersOf(calls: Array<[string, RequestInit | undefined]>): string {
  return calls
    .map(([, init]) => String((init?.headers as Record<string, string>)?.Authorization ?? ""))
    .join(" ");
}

test("queue sends the agent key, not the dashboard session token", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-queue-"));
  seedCreds(home, { apiKey: "sk_arena_theagentkey", accessToken: "dashboard-jwt" });
  const calls: Array<[string, RequestInit | undefined]> = [];
  const fetchStub = (async (url: unknown, init?: RequestInit) => {
    calls.push([String(url), init]);
    if (String(url).includes("/stakes")) {
      return json({ game: "goofspiel", tiers: [{ key: "low", label: "Low", coins: 100 }] });
    }
    return json({ status: "waiting" }, 202);
  }) as unknown as typeof fetch;

  await run(["queue", "goofspiel", "--tier", "low", "--api", "http://x"], { fetch: fetchStub, home });

  const sent = authHeadersOf(calls);
  assert.ok(
    sent.includes("sk_arena_theagentkey"),
    `queue did not carry the agent key — the server answers 403 forbidden_scope. Sent: ${sent}`,
  );
  assert.ok(!sent.includes("dashboard-jwt"), "queue carried the dashboard token, which the server rejects");
});

test("an explicit --token beats whatever is stored on the machine", async () => {
  const home = mkdtempSync(join(tmpdir(), "pyyol-queue-tok-"));
  seedCreds(home, { apiKey: "sk_arena_stored", accessToken: "dashboard-jwt" });
  const calls: Array<[string, RequestInit | undefined]> = [];
  const fetchStub = (async (url: unknown, init?: RequestInit) => {
    calls.push([String(url), init]);
    if (String(url).includes("/stakes")) {
      return json({ game: "goofspiel", tiers: [{ key: "low", label: "Low", coins: 100 }] });
    }
    return json({ status: "waiting" }, 202);
  }) as unknown as typeof fetch;

  await run(
    ["queue", "goofspiel", "--tier", "low", "--api", "http://x", "--token", "sk_arena_explicit"],
    { fetch: fetchStub, home },
  );

  const sent = authHeadersOf(calls);
  assert.ok(sent.includes("sk_arena_explicit"), `an explicit --token was ignored. Sent: ${sent}`);
  assert.ok(!sent.includes("sk_arena_stored"), "stored credentials overrode the explicit --token");
});
