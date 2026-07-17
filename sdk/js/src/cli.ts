#!/usr/bin/env node
/**
 * The `pyyol` CLI (JS/TS SDK) — parity with the Python CLI: login/logout/whoami,
 * init, dev (sandbox-locked dev loop), play (compete; --ranked = real stakes),
 * publish, replay, profile, leaderboard, arenas, doctor, update. Zero runtime deps:
 * uses Node 22+ globals (fetch, WebSocket) and built-ins only.
 */
import { existsSync } from "node:fs";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

import { asAgent } from "./adapter.js";
import * as config from "./config.js";
import * as creds from "./credentials.js";
import { deriveConnectUrl, runLoginFlow } from "./login.js";
import * as mode from "./mode.js";
import { RuntimeConnector } from "./runtime.js";
import { SDK_VERSION } from "./version.js";

const OK = "✓";
const BAD = "✗";

const PLAY_PATH: Record<string, string> = {
  goofspiel: "/v1/sandbox/pushplay",
  mafia: "/v1/mafia/pushplay",
  monopoly: "/v1/monopoly/pushplay",
};
const REPLAY_PATH: Record<string, string> = {
  goofspiel: "/v1/match/{id}/replay",
  mafia: "/v1/mafia/{id}/replay",
  monopoly: "/v1/monopoly/{id}/replay",
};

// ── arg parsing (tiny, dependency-free) ───────────────────────────────────────

interface Args {
  positionals: string[];
  flags: Record<string, string | boolean>;
}

function parse(argv: string[]): Args {
  const positionals: string[] = [];
  const flags: Record<string, string | boolean> = {};
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a.startsWith("--")) {
      const key = a.slice(2);
      const next = argv[i + 1];
      if (next !== undefined && !next.startsWith("--")) {
        flags[key] = next;
        i++;
      } else {
        flags[key] = true;
      }
    } else {
      positionals.push(a);
    }
  }
  return { positionals, flags };
}

const str = (a: Args, k: string, d = "") => (typeof a.flags[k] === "string" ? (a.flags[k] as string) : d);
const bool = (a: Args, k: string) => a.flags[k] === true || a.flags[k] === "true";
const num = (a: Args, k: string, d: number) => {
  const v = a.flags[k];
  return typeof v === "string" && /^\d+$/.test(v) ? parseInt(v, 10) : d;
};

// ── HTTP helpers (fetch) ───────────────────────────────────────────────────────

const HTTP_TIMEOUT_MS = 10_000; // fail fast on a stalled/low-bandwidth link, don't hang

let insecureWarned = false;
/** Warn (once) when credentials would go over cleartext non-loopback HTTP. */
function warnInsecureTransport(url: string, hasAuth: boolean): void {
  if (!hasAuth || insecureWarned) return;
  try {
    const u = new URL(url);
    if (u.protocol === "https:") return;
    const h = u.hostname.toLowerCase();
    if (h === "localhost" || h === "127.0.0.1" || h === "::1" || h.endsWith(".localhost")) return;
    insecureWarned = true;
    console.error(`${BAD} WARNING: sending credentials over insecure ${u.protocol}//${h} — use https://`);
  } catch {
    /* ignore */
  }
}

function netErr(e: unknown): string {
  const msg = String(e);
  return /timeout|abort/i.test(msg) ? "network timed out (slow or unreachable)" : msg;
}

let argvSecretWarned = false;
/** Warn (once) that a secret on the command line is visible to other users on a
 *  shared host (ps/proc). Prefer `pyyol login` (browser) or the PYYOL_TOKEN env var. */
function warnArgvSecret(): void {
  if (argvSecretWarned) return;
  argvSecretWarned = true;
  console.error(
    `${BAD} note: a secret on the command line is visible to other users on shared hosts ` +
      `(ps/proc). Prefer \`pyyol login\` (browser) or the PYYOL_TOKEN env var.`,
  );
}

async function apiGet(url: string, token = ""): Promise<[number, any]> {
  const headers: Record<string, string> = {};
  if (token) {
    headers.Authorization = "Bearer " + token;
    warnInsecureTransport(url, true);
  }
  try {
    const r = await fetch(url, { headers, signal: AbortSignal.timeout(HTTP_TIMEOUT_MS) });
    const text = await r.text();
    return [r.status, text ? JSON.parse(text) : {}];
  } catch (e) {
    return [0, { error: netErr(e) }];
  }
}

async function apiPost(url: string, token: string, body: unknown): Promise<[number, any]> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (token) {
    headers.Authorization = "Bearer " + token;
    warnInsecureTransport(url, true);
  }
  try {
    const r = await fetch(url, {
      method: "POST",
      headers,
      body: JSON.stringify(body ?? {}),
      signal: AbortSignal.timeout(HTTP_TIMEOUT_MS),
    });
    const text = await r.text();
    return [r.status, text ? JSON.parse(text) : {}];
  } catch (e) {
    return [0, { error: netErr(e) }];
  }
}

function httpBase(a: Args, c: creds.Credentials | null): string {
  if (str(a, "api")) return str(a, "api").replace(/\/$/, "");
  if (process.env.PYYOL_API) return process.env.PYYOL_API.replace(/\/$/, "");
  if (c?.url) return c.url.replace(/\/$/, "");
  if (c?.connectUrl) {
    try {
      const u = new URL(c.connectUrl);
      const scheme = u.protocol === "wss:" || u.protocol === "https:" ? "https:" : "http:";
      return `${scheme}//${u.host}`;
    } catch {
      /* ignore */
    }
  }
  return "";
}

// ── load the developer's agent from pyyol.toml ────────────────────────────────

async function loadAgentFromConfig(cfg: config.Config) {
  const { dirname, isAbsolute, relative } = await import("node:path");
  const [modPath, varName] = config.entryParts(cfg);
  // Constrain `entry` to the project root (dir of the discovered pyyol.toml): reject
  // absolute paths and `..` traversal so a hostile pyyol.toml can't load an arbitrary
  // file outside the project.
  const cfgFile = config.find();
  const root = cfgFile ? dirname(resolve(cfgFile)) : process.cwd();
  const abs = resolve(root, modPath);
  const rel = relative(root, abs);
  if (isAbsolute(modPath) || rel.startsWith("..")) {
    throw new Error(`entry ${modPath} must be inside the project (${root})`);
  }
  if (!existsSync(abs)) throw new Error(`entry module ${modPath} not found (see pyyol.toml entry)`);
  const mod = await import(pathToFileURL(abs).href);
  const obj = mod[varName] ?? mod.default;
  if (obj === undefined) throw new Error(`no \`${varName}\` export in ${modPath} (see pyyol.toml entry)`);
  return asAgent(obj);
}

// ── commands ───────────────────────────────────────────────────────────────────

async function cmdLogin(a: Args): Promise<number> {
  const token = str(a, "token");
  if (token) {
    warnArgvSecret();
    creds.save({
      url: str(a, "api"),
      connectUrl: str(a, "connect") || deriveConnectUrl(str(a, "api")),
      agentId: str(a, "agent"),
      accessToken: token,
      refreshToken: "",
    });
    console.log(`${OK} stored credentials`);
    return 0;
  }
  const dashboard = str(a, "dashboard") || str(a, "api");
  if (!dashboard) {
    console.error(`${BAD} pass --dashboard (or --api), or --token for headless login`);
    return 2;
  }
  const provider = str(a, "with");
  console.log(`opening ${dashboard}/cli-login in your browser${provider ? ` (via ${provider})` : ""}…`);
  try {
    const c = await runLoginFlow({ dashboardUrl: dashboard, apiUrl: str(a, "api"), provider });
    if (str(a, "connect")) c.connectUrl = str(a, "connect");
    creds.save(c);
    console.log(`${OK} logged in as ${c.agentId || "(no agent yet)"} — credentials stored`);
    return 0;
  } catch (e) {
    console.error(`${BAD} login failed: ${e}`);
    return 1;
  }
}

function cmdLogout(): number {
  console.log(creds.clear() ? `${OK} logged out` : "not logged in");
  return 0;
}

async function cmdWhoami(a: Args): Promise<number> {
  const c = creds.load();
  if (!c?.accessToken) {
    console.error(`${BAD} not logged in — run \`pyyol login\`.`);
    return 2;
  }
  const base = httpBase(a, c);
  const [, me] = base ? await apiGet(`${base}/v1/me`, c.accessToken) : [0, {}];
  console.log(`user      ${me.user_id ?? "(unknown)"}`);
  console.log(`agent     ${me.agent_id ?? c.agentId ?? "(none)"}`);
  console.log(`platform  ${c.url || base || "(unset)"}`);
  const cfg = config.load();
  if (cfg) console.log(`project   ${cfg.name}  ·  arena ${cfg.arena}  ·  mode ${cfg.mode}`);
  return 0;
}

function className(name: string): string {
  const parts = name.replace(/[^a-zA-Z0-9]+/g, " ").split(" ").filter(Boolean);
  const cls = parts.map((p) => p[0].toUpperCase() + p.slice(1)).join("") || "Agent";
  return /^\d/.test(cls) ? "A" + cls : cls;
}

async function cmdInit(a: Args): Promise<number> {
  const { mkdirSync, writeFileSync } = await import("node:fs");
  const { join } = await import("node:path");
  const dir = a.positionals[0];
  if (!dir) {
    console.error(`${BAD} usage: pyyol init <dir>`);
    return 2;
  }
  mkdirSync(dir, { recursive: true });
  const name = str(a, "name") || dir.split("/").filter(Boolean).pop() || "agent";
  const arena = str(a, "arena") || "goofspiel";
  const cls = className(name);
  const agentPath = join(dir, "agent.mjs");
  writeFileSync(
    agentPath,
    `import { Adapter } from "pyyol";

// ${name} — implement step(); initialize()/shutdown() are optional.
// Run:  pyyol dev            (practice, SANDBOX — no stakes)
//       pyyol play ${arena}  (compete; add --ranked for real, after \`pyyol publish\`)
class ${cls} extends Adapter {
  name = "${name}";
  supportedGames = ["${arena}"];

  step(view) {
    // Your strategy goes here (call any framework or LLM). Baseline below:
    const legal = view.legal_actions ?? [];
    ${arena === "goofspiel" ? "return { round: view.round, card: Math.min(...legal) };" : "return legal.length ? { action: legal[0] } : {};"}
  }
}

export const agent = new ${cls}();
`,
  );
  const cfg: config.Config = {
    ...config.defaults(),
    name,
    language: "javascript",
    framework: str(a, "framework"),
    arena,
    entry: "agent.mjs:agent",
  };
  const cfgPath = config.save(cfg, dir);
  console.log(`${OK} created javascript agent in ${dir}/`);
  console.log(`    ${agentPath}`);
  console.log(`    ${cfgPath}`);
  console.log("\nNext:");
  console.log("    npm install pyyol");
  console.log(`    cd ${dir} && pyyol dev            # practice locally (sandbox — no stakes)`);
  console.log(`    pyyol play ${arena}             # compete (sandbox); add --ranked for real`);
  return 0;
}

async function startSandbox(base: string, token: string, arena: string, label: string): Promise<void> {
  const path = PLAY_PATH[arena] ?? PLAY_PATH.goofspiel;
  for (let i = 0; i < 6; i++) {
    const [st, resp] = await apiPost(`${base}${path}`, token, {});
    if (st === 200 || st === 201) {
      const mid = resp.match_id ?? resp.id ?? "";
      console.log(`  ${OK} started ${arena} match ${mid} ${label}`.trimEnd());
      return;
    }
    const code = String(resp.code ?? resp.error ?? "");
    if (code.includes("transport") || code.includes("no_agent") || st === 409 || st === 425) {
      await new Promise((r) => setTimeout(r, 1500));
      continue;
    }
    console.log(`  ${BAD} could not start ${arena} match: ${JSON.stringify(resp)}`);
    return;
  }
}

async function orchestrate(a: Args, devLocked: boolean): Promise<number> {
  const cfg = config.load();
  if (!cfg) {
    console.error(`${BAD} no pyyol.toml here — run \`pyyol init <dir>\` first.`);
    return 2;
  }
  const c = creds.load();
  if (!c?.accessToken) {
    console.error(`${BAD} not logged in — run \`pyyol login\` first.`);
    return 2;
  }
  const connectUrl = str(a, "url") || process.env.PYYOL_URL || c.connectUrl;
  const base = httpBase(a, c);
  // The agent id MUST be the one the token belongs to. The token comes from creds,
  // so creds.agentId (its matched pair) wins over a possibly-stale pyyol.toml pin —
  // otherwise the socket's "key.agent == claimed agent_id" check rejects the register.
  const agentId = str(a, "agent") || c.agentId || cfg.agent_id;
  const token = str(a, "token") || process.env.PYYOL_TOKEN || c.accessToken;
  if (!connectUrl || !agentId) {
    console.error(`${BAD} missing connect URL or agent id — run \`pyyol login\` (or pass --url/--agent).`);
    return 2;
  }

  const arena = str(a, "arena") || (a.positionals[0] ?? "") || cfg.arena;
  const m = mode.resolveMode({ rankedFlag: bool(a, "ranked"), cfgMode: cfg.mode, devLocked });
  console.log(mode.banner(m));

  if (m === mode.RANKED) {
    if (!(await mode.confirmRanked(bool(a, "yes")))) {
      console.log("aborted — staying safe. (Use --yes in CI to skip the prompt.)");
      return 1;
    }
  }
  if (agentId && !cfg.agent_id) config.setAgentId(agentId);

  let agent;
  try {
    agent = await loadAgentFromConfig(cfg);
  } catch (e) {
    console.error(`${BAD} could not load your agent: ${e}`);
    return 2;
  }

  const quiet = bool(a, "quiet");
  const glyph: Record<string, string> = {
    connecting: "◔", connected: "●", reconnecting: "↻", turn: "→", event: "·", game_end: "★",
  };
  const feed = (kind: string, detail: string) => {
    if (quiet && kind === "turn") return; // keep lifecycle milestones even in --quiet
    const ts = new Date().toISOString().slice(11, 19);
    console.log(`${ts}  ${glyph[kind] ?? "·"}  ${kind.padEnd(10)} ${detail}`);
    if (kind === "connected") console.log(`${ts}  ◌  waiting    waiting for a match…`);
  };

  const conn = new RuntimeConnector(agent, {
    url: connectUrl,
    agentId,
    token,
    name: agent.name,
    games: agent.supportedGames,
    onFeed: feed,
  });

  // Kick match(es) after the socket registers; retry while it comes online.
  const matches = Math.max(1, num(a, "matches", devLocked ? 3 : 1));
  setTimeout(async () => {
    if (m === mode.RANKED) {
      const tier = str(a, "tier") || "low";
      const [st, resp] = await apiPost(`${base}/v1/queue`, token, { game: arena, tier });
      if (st === 200 || st === 202) console.log(`  ${OK} queued for RANKED ${arena} (tier ${tier})`);
      else if (String(resp.code ?? "").includes("certified"))
        console.log(`  ${BAD} agent not certified for ranked — run \`pyyol publish\` first.`);
      else console.log(`  ${BAD} could not queue ranked (${st}): ${JSON.stringify(resp)}`);
      return;
    }
    for (let i = 0; i < matches; i++) {
      await startSandbox(base, token, arena, `${i + 1}/${matches}`);
      await new Promise((r) => setTimeout(r, 2000));
    }
  }, 1500);

  process.on("SIGINT", () => {
    conn.stop();
  });
  try {
    await conn.run(); // blocks: connect + heartbeat + reconnect + serve turns
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    console.error(`\n${BAD} ${msg}`);
    if (/unauthorized|token|register/i.test(msg)) {
      console.error("  your token was rejected — run `pyyol login` again (agent id + agent key must match).");
    }
    return 1;
  }
  console.log("\nstopped.");
  return 0;
}

async function cmdArenas(a: Args): Promise<number> {
  const base = httpBase(a, creds.load());
  if (!base) {
    console.error(`${BAD} no API url — pass --api or run \`pyyol login\`.`);
    return 2;
  }
  const [st, resp] = await apiGet(`${base}/v1/arenas`);
  if (st !== 200) {
    console.error(`${BAD} could not fetch arenas (${st})`);
    return 1;
  }
  console.log("ARENA       PLAYERS   SANDBOX  RANKED  STATUS");
  for (const ar of resp.arenas ?? []) {
    const players = `${ar.min_players}-${ar.max_players}`;
    console.log(
      `${ar.id.padEnd(12)}${players.padEnd(10)}${(ar.sandbox ? "yes" : "no").padEnd(9)}` +
        `${(ar.ranked ? "yes" : "no").padEnd(8)}${ar.status}`,
    );
  }
  return 0;
}

async function cmdLeaderboard(a: Args): Promise<number> {
  const base = httpBase(a, creds.load());
  if (!base) {
    console.error(`${BAD} no API url — pass --api or run \`pyyol login\`.`);
    return 2;
  }
  if (bool(a, "developers")) {
    const q = str(a, "season") ? `?season=${str(a, "season")}` : "";
    const [, resp] = await apiGet(`${base}/v1/leaderboard/developers${q}`);
    console.log("#    DEVELOPER               P-INDEX");
    for (const r of resp.entries ?? [])
      console.log(`${String(r.rank).padEnd(5)}${String(r.username ?? r.developer ?? "?").padEnd(24)}${r.p_index}`);
    return 0;
  }
  const qs: string[] = [];
  if (str(a, "game")) qs.push(`game=${encodeURIComponent(str(a, "game"))}`);
  if (str(a, "season")) qs.push(`season=${str(a, "season")}`);
  const [st, resp] = await apiGet(`${base}/v1/leaderboard${qs.length ? "?" + qs.join("&") : ""}`);
  if (st !== 200) {
    console.error(`${BAD} could not fetch leaderboard (${st})`);
    return 1;
  }
  console.log("#    AGENT                   ELO    W-L-T");
  for (const r of resp.entries ?? [])
    console.log(
      `${String(r.rank).padEnd(5)}${String(r.name ?? r.slug ?? "?").padEnd(24)}${String(r.elo).padEnd(7)}${r.wins}-${r.losses}-${r.ties}`,
    );
  return 0;
}

async function cmdProfile(a: Args): Promise<number> {
  const c = creds.load();
  const base = httpBase(a, c);
  if (!base) {
    console.error(`${BAD} no API url — pass --api or run \`pyyol login\`.`);
    return 2;
  }
  let handle = a.positionals[0] ?? "";
  if (!handle) {
    const [, me] = await apiGet(`${base}/v1/me`, c?.accessToken ?? "");
    handle = me.user_id ?? "";
    if (!handle) {
      console.error(`${BAD} pass a handle: \`pyyol profile <@handle>\``);
      return 2;
    }
  }
  const [st, p] = await apiGet(`${base}/v1/developers/${encodeURIComponent(handle)}`);
  if (st !== 200) {
    console.error(`${BAD} no such developer ${handle} (${st}).`);
    return 1;
  }
  const dev = p.developer ?? {};
  const pidx = p.p_index ?? {};
  const stats = p.stats ?? {};
  console.log(`@${dev.username ?? dev.developer ?? "?"}`);
  if (pidx.p_index !== undefined)
    console.log(`  P-Index   ${pidx.p_index}  (rank #${pidx.global_rank}, top ${pidx.percentile}%)`);
  console.log(`  Record    ${stats.wins ?? 0}W-${stats.losses ?? 0}L-${stats.draws ?? 0}D over ${stats.total_matches ?? 0} matches`);
  if (stats.favorite_arena) console.log(`  Favorite  ${stats.favorite_arena}`);
  console.log(`  Agents    ${(p.agents ?? []).length}   Followers ${p.followers ?? 0}`);
  return 0;
}

function winnerLabel(w: unknown): string {
  if (w === null || w === undefined || w === "") return "";
  if (typeof w === "number") return w < 0 ? "tie" : `seat ${w}`;
  return String(w);
}

function replayOutcome(resp: any): [string, number[] | null] {
  for (const k of ["winner", "winner_team", "winner_agent"]) {
    if (resp[k] !== undefined && resp[k] !== "") return [winnerLabel(resp[k]), null];
  }
  const events = resp.events ?? [];
  for (let i = events.length - 1; i >= 0; i--) {
    const ev = events[i];
    const payload = ev && typeof ev.payload === "object" ? ev.payload : {};
    if (["match_finished", "game_over", "victory", "finished"].includes(ev?.type) || "winner" in payload) {
      return [winnerLabel(payload.winner), payload.scores ?? null];
    }
  }
  return ["", null];
}

async function cmdReplay(a: Args): Promise<number> {
  const c = creds.load();
  const base = httpBase(a, c);
  if (!base) {
    console.error(`${BAD} no API url — pass --api or run \`pyyol login\`.`);
    return 2;
  }
  const match = a.positionals[0];
  if (!match) {
    console.error(`${BAD} usage: pyyol replay <match_id>`);
    return 2;
  }
  const cfg = config.load();
  const game = str(a, "game") || cfg?.arena || "goofspiel";
  const path = (REPLAY_PATH[game] ?? REPLAY_PATH.goofspiel).replace("{id}", encodeURIComponent(match));
  const [st, resp] = await apiGet(`${base}${path}`);
  if (st !== 200) {
    console.error(`${BAD} could not fetch replay (${st})`);
    return 1;
  }
  if (bool(a, "json")) {
    console.log(JSON.stringify(resp, null, 2));
    return 0;
  }
  const events = resp.events ?? resp.moves ?? [];
  const [winner, scores] = replayOutcome(resp);
  console.log(`replay ${match} (${game}) — ${events.length} events, status ${resp.status ?? "?"}`);
  if (winner) console.log(`  winner    ${winner}${scores ? `  (scores ${scores.join("-")})` : ""}`);
  if (resp.moves_verified !== undefined) console.log(`  verified  ${resp.moves_verified} (every move signed + valid)`);
  console.log(`  full JSON: pyyol replay ${match} --game ${game} --json`);
  return 0;
}

async function cmdDoctor(a: Args): Promise<number> {
  const checks: [string, boolean, string][] = [];
  const c = creds.load();
  checks.push(["logged in", Boolean(c?.accessToken), c?.url || "run `pyyol login`"]);
  const cfg = config.load();
  if (!cfg) {
    checks.push(["pyyol.toml", false, "run `pyyol init`"]);
  } else {
    const problems = config.validate(cfg);
    checks.push(["pyyol.toml", problems.length === 0, problems.join("; ") || `${cfg.name} · ${cfg.arena} · ${cfg.mode}`]);
    try {
      await loadAgentFromConfig(cfg);
      checks.push(["agent loads", true, cfg.entry]);
    } catch (e) {
      checks.push(["agent loads", false, String(e)]);
    }
  }
  const base = httpBase(a, c);
  if (base) {
    const [st] = await apiGet(`${base}/v1/arenas`);
    checks.push(["platform reachable", st === 200, `${base} (${st})`]);
  } else {
    checks.push(["platform reachable", false, "no API url"]);
  }
  checks.push(["sdk version", true, SDK_VERSION]);

  console.log("pyyol doctor\n");
  let allOk = true;
  for (const [name, ok, detail] of checks) {
    allOk = allOk && ok;
    console.log(`  ${ok ? OK : BAD} ${name.padEnd(20)} ${detail}`);
  }
  console.log("\n" + (allOk ? "✓ ready — `pyyol dev` to practice, `pyyol play <arena>` to compete." : "fix the ✗ items above."));
  return allOk ? 0 : 1;
}

async function cmdUpdate(): Promise<number> {
  console.log(`pyyol ${SDK_VERSION}`);
  try {
    const r = await fetch("https://registry.npmjs.org/pyyol/latest", { signal: AbortSignal.timeout(5000) });
    const latest = (await r.json())?.version;
    if (latest && latest !== SDK_VERSION) {
      console.log(`  update available: ${latest}`);
      console.log("  run:  npm install -g pyyol@latest");
    } else if (latest) {
      console.log("  you're up to date.");
    }
  } catch {
    console.log("  run:  npm install -g pyyol@latest");
  }
  return 0;
}

async function cmdPublish(a: Args): Promise<number> {
  const { readFileSync } = await import("node:fs");
  const c = creds.load();
  const base = httpBase(a, c);
  const agent = str(a, "agent") || c?.agentId || "";
  const token = str(a, "token") || c?.accessToken || "";
  const manifest = str(a, "manifest");
  if (!base || !agent || !token || !manifest) {
    console.error(`${BAD} need --manifest (and --api/--agent/--token or \`pyyol login\`)`);
    return 2;
  }
  if (str(a, "token") || str(a, "secret")) warnArgvSecret();
  const ag = encodeURIComponent(agent); // never interpolate a raw id into the path
  const body = readFileSync(manifest, "utf8");
  const [st1, m] = await apiPost(`${base}/v1/agents/${ag}/manifest`, token, JSON.parse(body));
  if (st1 !== 201) {
    console.error(`${BAD} submit failed (${st1}): ${JSON.stringify(m)}`);
    return 1;
  }
  const mid = encodeURIComponent(String(m.manifest_id ?? ""));
  console.log(`${OK} manifest submitted: ${m.manifest_id}`);
  if (str(a, "secret")) {
    const [st2, r] = await apiRequest("PUT", `${base}/v1/agents/${ag}/manifest/${mid}/endpoint-secret`, token, {
      token: str(a, "secret"),
    });
    if (st2 !== 200) {
      console.error(`${BAD} set endpoint secret failed (${st2}): ${JSON.stringify(r)}`);
      return 1;
    }
    console.log(`${OK} endpoint secret stored`);
  }
  const [st3, report] = await apiPost(`${base}/v1/agents/${ag}/manifest/${mid}/verify`, token, {});
  const verified = st3 === 200 && (report.verified || report.status === "verified");
  console.log(`${verified ? OK : BAD} verify (${st3}): ${JSON.stringify(report)}`);
  return verified ? 0 : 1;
}

async function apiRequest(method: string, url: string, token: string, body: unknown): Promise<[number, any]> {
  warnInsecureTransport(url, Boolean(token));
  try {
    const r = await fetch(url, {
      method,
      headers: { "Content-Type": "application/json", Authorization: "Bearer " + token },
      body: JSON.stringify(body ?? {}),
      signal: AbortSignal.timeout(HTTP_TIMEOUT_MS),
    });
    const text = await r.text();
    return [r.status, text ? JSON.parse(text) : {}];
  } catch (e) {
    return [0, { error: netErr(e) }];
  }
}

const HELP = `pyyol — build, run, and rank autonomous AI agents.
Quickstart: pyyol login → pyyol init <dir> → pyyol dev

Commands:
  login [--with github|google|wallet] [--dashboard URL] [--token PAT]
  logout
  whoami
  init <dir> [--arena goofspiel|mafia|monopoly] [--framework F] [--name N]
  dev [--matches N]                 local dev loop — SANDBOX, no stakes
  play <arena> [--ranked] [--tier]  compete; --ranked = real stakes
  publish                           (advanced) certify for ranked
  replay <match_id> [--game] [--json]
  profile [handle]
  leaderboard [--game G] [--developers] [--season N]
  arenas
  doctor
  update
`;

export async function main(argv = process.argv.slice(2)): Promise<number> {
  const command = argv[0];
  const a = parse(argv.slice(1));
  switch (command) {
    case "login":
      return cmdLogin(a);
    case "logout":
      return cmdLogout();
    case "whoami":
      return cmdWhoami(a);
    case "init":
      return cmdInit(a);
    case "dev":
      return orchestrate(a, true);
    case "play":
      return orchestrate(a, false);
    case "publish":
      return cmdPublish(a);
    case "arenas":
      return cmdArenas(a);
    case "leaderboard":
      return cmdLeaderboard(a);
    case "profile":
      return cmdProfile(a);
    case "replay":
      return cmdReplay(a);
    case "doctor":
      return cmdDoctor(a);
    case "update":
      return cmdUpdate();
    case "--version":
    case "-v":
      console.log(`pyyol ${SDK_VERSION}`);
      return 0;
    case undefined:
    case "-h":
    case "--help":
    case "help":
      console.log(HELP);
      return 0;
    default:
      console.error(`${BAD} unknown command: ${command}\n`);
      console.log(HELP);
      return 2;
  }
}

// Run when invoked as the bin (not when imported by tests).
const invoked = process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href;
if (invoked) {
  // Set exitCode and let the event loop drain — process.exit() can truncate a
  // large piped stdout (e.g. `pyyol replay … --json | jq`) mid-write.
  main()
    .then((code) => {
      process.exitCode = code;
    })
    .catch((e) => {
      console.error(`${BAD} ${e instanceof Error ? e.message : String(e)}`);
      process.exitCode = 1;
    });
}
