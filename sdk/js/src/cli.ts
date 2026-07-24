#!/usr/bin/env node
/**
 * The `pyyol` CLI (JS/TS SDK) — parity with the Python CLI: login/logout/whoami,
 * init, dev (sandbox-locked dev loop), play (compete; --ranked = real stakes),
 * publish, replay, profile, leaderboard, arenas, doctor, update. Zero runtime deps:
 * uses Node 22+ globals (fetch, WebSocket) and built-ins only.
 */
import { existsSync, realpathSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import { asAgent } from "./adapter.js";
import * as config from "./config.js";
import * as creds from "./credentials.js";
import { enableGateway } from "./instrument.js";
import { deriveConnectUrl, runLoginFlow } from "./login.js";
import * as mode from "./mode.js";
import { RuntimeConnector } from "./runtime.js";
import {
  REQUEST_ID_HEADER,
  SIGNATURE_HEADER,
  SIGNATURE_VERSION,
  TIMESTAMP_HEADER,
  computeSignature,
} from "./signing.js";
import { SDK_VERSION } from "./version.js";

const OK = "✓";
const BAD = "✗";
const WARN = "•";

// Public platform defaults. `pyyol login` with no flags hits the live platform;
// self-hosted/local users override via PYYOL_API / PYYOL_DASHBOARD (or --api /
// --dashboard). The API host serves /v1/*; the dashboard host serves /cli-login —
// DIFFERENT hosts in the split-domain deployment, so dashboard must not fall back
// to the API host.
const DEFAULT_API_BASE = (process.env.PYYOL_API || "").replace(/\/$/, "") || "https://api.pyyol.com";
const DEFAULT_DASHBOARD = (process.env.PYYOL_DASHBOARD || "").replace(/\/$/, "") || "https://pyyol.com";
// Verified-tier LLM gateway base (Phase 4). Ranked mode enables gateway routing so
// pyyol.route(client) sends the agent's LLM calls through it for server-observed
// (unfakeable) model/token/cost. Override with $PYYOL_GATEWAY.
const DEFAULT_GATEWAY = (process.env.PYYOL_GATEWAY || "").replace(/\/$/, "") || "https://gateway.pyyol.com";

// Agent API keys look like "sk_arena_<lookup>_<secret>" — the long-lived, revocable
// connection credential (mirrors backend platform.PrefixKey).
const AGENT_KEY_PREFIX = "sk_arena_";

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

export interface Args {
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

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// Rate-limited endpoints (manifest verify, deposits, withdrawals — which `publish`
// hits) answer 429 with a Retry-After header (seconds). Retry a small, bounded
// number of times so a transient limit doesn't fail the command outright.
const RETRY_MAX = 2; // retries after the first attempt → 3 total
const RETRY_AFTER_CAP_MS = 30_000; // never honor a Retry-After longer than this
const RETRY_BACKOFF_MS = [500, 1000]; // fallback when no Retry-After header

/** Parse a Retry-After header (delta-seconds) to ms, capped; null if absent/unparseable. */
function retryAfterMs(header: string | null): number | null {
  if (!header) return null;
  const secs = Number(header.trim());
  if (!Number.isFinite(secs) || secs < 0) return null;
  return Math.min(secs * 1000, RETRY_AFTER_CAP_MS);
}

/** Run a fetch thunk, retrying ONLY on HTTP 429 up to RETRY_MAX times (3 total).
 *  Sleeps for Retry-After (capped) when present, else exponential backoff. Returns
 *  the final Response (including a 429 once the cap is reached). The thunk builds a
 *  fresh init each call so every attempt gets its own AbortSignal.timeout. */
async function fetchWithRetry(doFetch: () => Promise<Response>): Promise<Response> {
  let res = await doFetch();
  for (let attempt = 0; res.status === 429 && attempt < RETRY_MAX; attempt++) {
    const wait = retryAfterMs(res.headers.get("retry-after")) ?? RETRY_BACKOFF_MS[attempt];
    await sleep(wait);
    res = await doFetch();
  }
  return res;
}

export async function apiGet(url: string, token = ""): Promise<[number, any]> {
  const headers: Record<string, string> = {};
  if (token) {
    headers.Authorization = "Bearer " + token;
    warnInsecureTransport(url, true);
  }
  try {
    const r = await fetchWithRetry(() => fetch(url, { headers, signal: AbortSignal.timeout(HTTP_TIMEOUT_MS) }));
    const text = await r.text();
    return [r.status, text ? JSON.parse(text) : {}];
  } catch (e) {
    return [0, { error: netErr(e) }];
  }
}

export async function apiPost(url: string, token: string, body: unknown): Promise<[number, any]> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  if (token) {
    headers.Authorization = "Bearer " + token;
    warnInsecureTransport(url, true);
  }
  try {
    const r = await fetchWithRetry(() =>
      fetch(url, {
        method: "POST",
        headers,
        body: JSON.stringify(body ?? {}),
        signal: AbortSignal.timeout(HTTP_TIMEOUT_MS),
      }),
    );
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
  return DEFAULT_API_BASE;
}

/** The credential the agent CONNECTION registers with, and whether it's the
 *  long-lived agent key. Prefer the agent key (`sk_arena_…`, no timer expiry) so
 *  the connection persists forever — like an OpenAI/`gh` token — falling back to
 *  the short-lived dashboard JWT (which the connector then auto-refreshes).
 *  Mirrors the Python `_connection_token`. */
export function connectionToken(a: Args, c: creds.Credentials | null): { token: string; usingAgentKey: boolean } {
  const explicit = str(a, "token") || process.env.PYYOL_TOKEN || "";
  if (explicit) return { token: explicit, usingAgentKey: explicit.startsWith(AGENT_KEY_PREFIX) };
  if (c?.apiKey) return { token: c.apiKey, usingAgentKey: true };
  if (c) return { token: c.accessToken, usingAgentKey: false };
  return { token: "", usingAgentKey: false };
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
  // API host serves /v1/*; dashboard host serves /cli-login — different in prod,
  // so dashboard must NOT fall back to --api. Both default to the live platform.
  const api = (str(a, "api") || DEFAULT_API_BASE).replace(/\/$/, "");
  const dashboard = (str(a, "dashboard") || DEFAULT_DASHBOARD).replace(/\/$/, "");
  if (token) {
    warnArgvSecret();
    creds.save({
      url: api,
      connectUrl: str(a, "connect") || deriveConnectUrl(api),
      agentId: str(a, "agent"),
      accessToken: token,
      refreshToken: "",
      // An explicit sk_arena_… token IS the persistent agent key; a dashboard JWT isn't.
      apiKey: token.startsWith(AGENT_KEY_PREFIX) ? token : "",
    });
    console.log(`${OK} stored credentials`);
    return 0;
  }
  const provider = str(a, "with");
  console.log(`opening ${dashboard}/cli-login in your browser${provider ? ` (via ${provider})` : ""}…`);
  try {
    const c = await runLoginFlow({ dashboardUrl: dashboard, apiUrl: api, provider });
    if (str(a, "connect")) c.connectUrl = str(a, "connect");
    // Mint a long-lived agent key for THIS machine (unless the dashboard already
    // handed one back). This is the credential the agent connection uses — like an
    // OpenAI/`gh` token, it never expires on a timer, so `pyyol dev`/`serve` keeps
    // working forever until you revoke it, re-login elsewhere, or lose the machine.
    // Best-effort: if it fails we still store the session and fall back to the
    // short-lived JWT + refresh for the connection.
    if (!c.apiKey && c.agentId && c.accessToken) {
      const [st, resp] = await apiPost(`${api}/v1/agent/keys`, c.accessToken, { agent_id: c.agentId });
      if (st === 201 && resp.api_key) c.apiKey = resp.api_key;
      else console.error(`note: couldn't mint a persistent agent key (${st}); using the refreshable session instead.`);
    }
    creds.save(c);
    const persist = c.apiKey ? " · persistent agent key" : "";
    console.log(`${OK} logged in as ${c.agentId || "(no agent yet)"} — credentials stored${persist}`);
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
  // Persistent agent key ⇒ the connection never needs re-login (revoke/PC-change
  // only); otherwise the session rides the refreshable dashboard token.
  console.log(`session   ${c.apiKey ? "persistent agent key" : "refreshable token"}`);
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
  // The connection runs on the agent key OR the dashboard JWT — either proves a
  // session. (The agent key is the persistent, no-expiry one.)
  if (!c || !(c.accessToken || c.apiKey)) {
    console.error(`${BAD} not logged in — run \`pyyol login\` first.`);
    return 2;
  }
  const connectUrl = str(a, "url") || process.env.PYYOL_URL || c.connectUrl;
  const base = httpBase(a, c);
  // The agent id MUST be the one the token belongs to. The token comes from creds,
  // so creds.agentId (its matched pair) wins over a possibly-stale pyyol.toml pin —
  // otherwise the socket's "key.agent == claimed agent_id" check rejects the register.
  const agentId = str(a, "agent") || c.agentId || cfg.agent_id;
  const { token, usingAgentKey } = connectionToken(a, c);
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
    // Enable verified-tier gateway routing (only with an agent key — the gateway
    // authenticates X-Pyyol-Key via it; a dashboard JWT can't). Then pyyol.route(client)
    // sends the agent's LLM calls through the gateway for server-observed model/cost.
    if (usingAgentKey && token) {
      enableGateway(token, DEFAULT_GATEWAY);
      console.log(`  ${OK} verified gateway routing on (${DEFAULT_GATEWAY}) — call pyyol.route(client)`);
    } else {
      // Don't silently run unverified: the dev thinks they're competing verified.
      console.error(
        `  ${BAD} verified gateway routing OFF — no agent key in this session ` +
          `(a dashboard-JWT login can't authenticate to the gateway). Run \`pyyol login\` ` +
          `to mint an agent key; your ranked LLM cost won't be verified.`,
      );
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

  const feed = makeFeed(bool(a, "quiet"));

  const conn = new RuntimeConnector(agent, {
    url: connectUrl,
    agentId,
    token,
    name: agent.name,
    games: agent.supportedGames,
    onFeed: feed,
    // Silent access-token refresh: on a rejected register, spend the rotating
    // refresh token for a fresh access token and persist the pair back to creds —
    // keeps a long `pyyol dev`/`play` authenticated past the short access TTL. The
    // agent key can't expire, so refresh is only wired when NOT using it.
    ...(usingAgentKey ? {} : refreshOpts(c, base)),
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

/** `pyyol wallet` — show the owner's coin balance + per-agent wallets, so a dev can
 *  see why ranked was refused ("not enough coins") without leaving the CLI. Parity
 *  with the Python CLI. Owner-scoped, so it uses the dashboard access token. */
async function cmdWallet(a: Args): Promise<number> {
  const c = creds.load();
  const base = httpBase(a, c);
  const token = c?.accessToken || str(a, "token") || process.env.PYYOL_TOKEN || "";
  if (!token) {
    console.error(`${BAD} not logged in — run \`pyyol login\` first.`);
    return 2;
  }
  const [st, w] = await apiGet(`${base}/v1/user/wallet`, token);
  if (st !== 200) {
    console.error(`${BAD} could not fetch wallet (${st}): ${JSON.stringify(w)}`);
    return 1;
  }
  if (bool(a, "json")) {
    console.log(JSON.stringify(w, null, 2));
    return 0;
  }
  const cents = Number(w.coin_cents ?? 1) || 1;
  const usd = (coins: number) => `$${((coins * cents) / 100).toFixed(2)}`;
  const avail = Number(w.available_balance ?? 0);
  console.log("Treasury");
  console.log(`  Available   ${avail.toLocaleString()} coins  (${usd(avail)})`);
  if (w.locked_balance) console.log(`  Locked      ${Number(w.locked_balance).toLocaleString()} coins (in active matches)`);
  if (w.lifetime_earnings) console.log(`  Earned      ${Number(w.lifetime_earnings).toLocaleString()} coins (lifetime)`);
  const agents = w.agents ?? [];
  if (agents.length) {
    console.log("\nAgent wallets");
    for (const ag of agents) {
      const bal = Number(ag.balance ?? 0).toLocaleString();
      const wd = Number(ag.withdrawable ?? 0).toLocaleString();
      console.log(`  ${String(ag.name ?? ag.agent ?? "?").padEnd(20)} ${bal.padStart(10)} coins   withdrawable ${wd}`);
    }
  }
  return 0;
}

/** `pyyol queue <game> [--tier low|mid|high | --bid N] [--list]` — enter ranked
 *  matchmaking at a stake tier (parity with the Python CLI). `--list` shows the
 *  admin-configured tiers. The game is a POSITIONAL argument. */
async function cmdQueue(a: Args): Promise<number> {
  const c = creds.load();
  const base = httpBase(a, c);
  if (!base) {
    console.error(`${BAD} no API url — pass --api or run \`pyyol login\`.`);
    return 2;
  }
  const game = a.positionals[0] ?? "";
  if (!game) {
    console.error(`${BAD} usage: pyyol queue <game> [--tier low|mid|high | --bid N] [--list]`);
    return 2;
  }
  if (bool(a, "list")) {
    const [st, resp] = await apiGet(`${base}/v1/games/${game}/stakes`);
    if (st !== 200) {
      console.error(`${BAD} could not fetch tiers (${st})`);
      return 1;
    }
    const tiers = resp.tiers ?? [];
    if (!tiers.length) {
      console.log(`no stake tiers configured for ${game} — use --bid <coins>`);
      return 0;
    }
    console.log(`${game} stake tiers:`);
    for (const t of tiers) console.log(`  ${String(t.key ?? "").padEnd(8)} ${String(Number(t.coins ?? 0)).padStart(8)} coins  ${t.label ?? ""}`);
    return 0;
  }
  const token = c?.accessToken || str(a, "token") || process.env.PYYOL_TOKEN || "";
  if (!token) {
    console.error(`${BAD} not logged in — run \`pyyol login\` first.`);
    return 2;
  }
  const body: Record<string, unknown> = { game };
  if (str(a, "tier")) body.tier = str(a, "tier");
  else if (num(a, "bid", 0) > 0) body.bid = num(a, "bid", 0);
  else {
    console.error(`${BAD} choose a stake: --tier <low|mid|high> (see \`pyyol queue ${game} --list\`) or --bid <coins>`);
    return 2;
  }
  const [st, resp] = await apiPost(`${base}/v1/queue`, token, body);
  if (st !== 200 && st !== 202) {
    const code = String(resp.code ?? resp.error ?? "");
    if (code.includes("certified")) console.error(`${BAD} agent not certified — run \`pyyol publish --manifest <file>\` first.`);
    else if (code.includes("balance") || code.includes("insufficient")) console.error(`${BAD} not enough coins — fund your wallet (see \`pyyol wallet\`).`);
    else console.error(`${BAD} could not queue ranked (${st}): ${JSON.stringify(resp)}`);
    return 1;
  }
  console.log(`  ${OK} queued for ${game}${body.tier ? ` (tier ${body.tier})` : ""} — keep your agent connected; it plays when matched.`);
  if (resp.match_id) console.log(`  ${OK} matched → ${resp.match_id}\n      watch it:  pyyol watch ${resp.match_id}`);
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

export async function apiRequest(method: string, url: string, token: string, body: unknown): Promise<[number, any]> {
  warnInsecureTransport(url, Boolean(token));
  try {
    const r = await fetchWithRetry(() =>
      fetch(url, {
        method,
        headers: { "Content-Type": "application/json", Authorization: "Bearer " + token },
        body: JSON.stringify(body ?? {}),
        signal: AbortSignal.timeout(HTTP_TIMEOUT_MS),
      }),
    );
    const text = await r.text();
    return [r.status, text ? JSON.parse(text) : {}];
  } catch (e) {
    return [0, { error: netErr(e) }];
  }
}

// ── shared: live feed + token-refresh options (used by orchestrate/run) ────────

/** Build the one-line lifecycle feed printer shared by `dev`/`play`/`run`. */
function makeFeed(quiet: boolean): (kind: string, detail: string) => void {
  const glyph: Record<string, string> = {
    connecting: "◔", connected: "●", reconnecting: "↻", reauth: "🔑", turn: "→", event: "·", game_end: "★",
  };
  return (kind: string, detail: string) => {
    if (quiet && kind === "turn") return; // keep lifecycle milestones even in --quiet
    const ts = new Date().toISOString().slice(11, 19);
    console.log(`${ts}  ${glyph[kind] ?? "·"}  ${kind.padEnd(10)} ${detail}`);
    if (kind === "connected") console.log(`${ts}  ◌  waiting    waiting for a match…`);
  };
}

/** Connector kwargs enabling silent access-token refresh from stored creds. The
 *  onTokens callback writes the rotated pair back so a long run stays authed past
 *  the short access-token TTL. */
function refreshOpts(c: creds.Credentials | null, base: string) {
  return {
    refreshToken: c?.refreshToken,
    apiUrl: base,
    onTokens: (access: string, refresh: string) => {
      if (!c) return;
      c.accessToken = access;
      if (refresh) c.refreshToken = refresh;
      creds.save(c);
    },
  };
}

/** Import a developer's agent from an explicit file (used by `run`/`serve`), then
 *  normalize it to an Agent. Mirrors loadAgentFromConfig but file-based. */
async function loadAgentFromFile(file: string, varName: string) {
  const abs = resolve(file);
  if (!existsSync(abs)) throw new Error(`cannot load ${file}`);
  const mod = await import(pathToFileURL(abs).href);
  const obj = mod[varName] ?? mod.default;
  if (obj === undefined) throw new Error(`no \`${varName}\` found in ${file} (expose your Agent as \`${varName}\`)`);
  return asAgent(obj);
}

// ── status ─────────────────────────────────────────────────────────────────────

async function cmdStatus(a: Args): Promise<number> {
  const c = creds.load();
  if (!c?.accessToken) {
    console.error(`${BAD} not logged in — run \`pyyol login\` first`);
    return 2;
  }
  const api = (str(a, "api") || c.url).replace(/\/$/, "");
  const agentId = str(a, "agent") || c.agentId;
  if (!api || !agentId) {
    console.error(`${BAD} need an API url and agent id (login or pass --api/--agent)`);
    return 2;
  }
  const [st, body] = await apiGet(`${api}/v1/agent/status?agent_id=${encodeURIComponent(agentId)}`, c.accessToken);
  if (st !== 200) {
    console.error(`${BAD} status failed (${st}): ${JSON.stringify(body)}`);
    return 1;
  }
  const online = Boolean(body.online);
  console.log(`Agent ${agentId}\n  ${online ? "🟢 Online" : "⚪ Offline"}`);
  if (online) {
    console.log(`  SDK       ${body.sdk_version ?? "?"}`);
    console.log(`  Games     ${(body.games ?? []).join(", ")}`);
    console.log(`  Last seen ${body.last_seen ?? "?"}`);
  }
  return 0;
}

// ── autoplay (toggle hosted auto-play without holding a connection) ─────────────

/** Resolve (mode, games) from flags → pyyol.toml → defaults. */
function autoplayOpts(a: Args, cfg: config.Config | null): [string, string[]] {
  const m = bool(a, "ranked") ? "ranked" : str(a, "mode") || cfg?.mode || "sandbox";
  let games = str(a, "games").split(",").map((g) => g.trim()).filter(Boolean);
  if (games.length === 0 && cfg?.arena) games = [cfg.arena];
  return [m, games];
}

/** PUT the agent's auto-play availability. Matches the Python `_autoplay_set`
 *  (PUT /v1/agent/autoplay {enabled, mode, bid, games}). */
async function autoplaySet(
  api: string,
  token: string,
  enabled: boolean,
  m: string,
  bid: number,
  games: string[],
): Promise<[number, any]> {
  return apiRequest("PUT", `${api.replace(/\/$/, "")}/v1/agent/autoplay`, token, { enabled, mode: m, bid, games });
}

/** GET the agent's auto-play setting + last observed status (mirrors Python
 *  `_autoplay_get`). */
async function autoplayGet(api: string, token: string): Promise<[number, any]> {
  return apiGet(`${api.replace(/\/$/, "")}/v1/agent/autoplay`, token);
}

const AUTOPLAY_STATUS_LABEL: Record<string, [string, string]> = {
  playing: [OK, "playing"],
  searching: [OK, "searching for an opponent"],
  paused: [WARN, "paused"],
  blocked: [BAD, "not playing"],
};

/** Render `pyyol autoplay status` — is it on, and WHY it is or isn't playing, so a
 *  quiet auto-play agent is never a mystery. */
function printAutoplayStatus(body: any): void {
  if (!body?.enabled) {
    console.log(`${WARN} auto-play is OFF (turn it on with \`pyyol autoplay on\`)`);
    return;
  }
  console.log(`${OK} auto-play is ON — mode=${body.mode || "sandbox"}`);
  const status: string = body.last_status || "";
  const reason: string = body.last_status_reason || "";
  if (!status) {
    console.log("  status: starting up — no activity recorded yet (check back in a moment)");
    return;
  }
  const [marker, label] = AUTOPLAY_STATUS_LABEL[status] ?? [WARN, status];
  console.log(`  ${marker} ${label}${reason ? ` — ${reason}` : ""}`);
  if (body.last_status_at) console.log(`  as of ${body.last_status_at}`);
  if (status === "blocked")
    console.log("  fix the reason above (e.g. connect your agent with `pyyol run`), and it resumes automatically.");
}

async function cmdAutoplay(a: Args): Promise<number> {
  const state = a.positionals[0];
  if (state !== "on" && state !== "off" && state !== "status") {
    console.error(`${BAD} usage: pyyol autoplay on|off|status`);
    return 2;
  }
  const c = creds.load();
  const api = (str(a, "api") || c?.url || "").replace(/\/$/, "");
  // Auto-play is agent-scoped (/v1/agent/autoplay), so it needs the agent key.
  const { token } = connectionToken(a, c);
  if (!api || !token) {
    console.error(`${BAD} run \`pyyol login\` first`);
    return 2;
  }
  if (str(a, "token")) warnArgvSecret();
  // `pyyol autoplay status` READS the current state + why it is/isn't playing.
  if (state === "status") {
    const [st, resp] = await autoplayGet(api, token);
    if (st >= 200 && st < 300) {
      printAutoplayStatus(resp);
      return 0;
    }
    console.error(`${BAD} failed (status ${st}): ${JSON.stringify(resp)}`);
    return 1;
  }
  const on = state === "on";
  const [m, games] = autoplayOpts(a, config.load());
  const [st, resp] = await autoplaySet(api, token, on, m, num(a, "bid", 0), games);
  if (st >= 200 && st < 300) {
    const detail = on ? ` — mode=${m}, games=${games.length ? games.join(",") : "default"}` : "";
    console.log(`${OK} auto-play ${on ? "ON" : "OFF"}${detail}`);
    return 0;
  }
  console.error(`${BAD} failed (status ${st}): ${JSON.stringify(resp)}`);
  return 1;
}

// ── logs (tail the local run log) ───────────────────────────────────────────────

async function cmdLogs(a: Args): Promise<number> {
  const { existsSync: exists, readFileSync } = await import("node:fs");
  const { join } = await import("node:path");
  const path = str(a, "file") || join(creds.configDir(), "logs", "agent.log");
  if (!exists(path)) {
    console.log(`no logs yet at ${path} (run \`pyyol run\` to generate them)`);
    return 0;
  }
  const lines = readFileSync(path, "utf8").split("\n");
  if (lines.length && lines[lines.length - 1] === "") lines.pop(); // drop trailing newline's empty tail
  const n = num(a, "n", 200);
  for (const line of lines.slice(-n)) console.log(line);
  return 0;
}

// ── simulate (local in-process Goofspiel match against the configured agent) ────

async function cmdSimulate(a: Args): Promise<number> {
  const game = str(a, "game") || "goofspiel";
  if (game !== "goofspiel") {
    console.error(`simulate currently supports goofspiel (got '${game}'); use \`validate\` for a single-turn check of any game.`);
    return 2;
  }
  const opponent = str(a, "opponent") || "baseline";
  if (opponent !== "baseline") {
    console.error(`${BAD} unknown opponent '${opponent}' — only 'baseline' is supported`);
    return 2;
  }
  const cfg = config.load();
  if (!cfg) {
    console.error(`${BAD} no pyyol.toml here — run \`pyyol init <dir>\` first.`);
    return 2;
  }
  let agent;
  try {
    agent = await loadAgentFromConfig(cfg);
  } catch (e) {
    console.error(`${BAD} could not load your agent: ${e}`);
    return 2;
  }
  const { simulateGoofspiel, SimulationError } = await import("./simulator.js");
  try {
    const r = await simulateGoofspiel(agent, { handSize: num(a, "rounds", 13), seed: num(a, "seed", 1) });
    console.log(
      `simulate goofspiel (${r.rounds} rounds): winner=${r.winner} scores agent=${r.scores.agent} baseline=${r.scores.baseline}`,
    );
    return 0;
  } catch (e) {
    if (e instanceof SimulationError) {
      console.error(`${BAD} ${e.message}`);
      return 1;
    }
    throw e;
  }
}

// ── validate (probe a hosted endpoint like the platform does) ───────────────────

function rfc3339(): string {
  return new Date().toISOString().replace(/\.\d+Z$/, "Z");
}

/** Derive {dir(url)}/name — matches the platform's sibling routing. */
function sibling(url: string, name: string): string {
  const trimmed = url.replace(/\/+$/, "");
  const idx = trimmed.lastIndexOf("/");
  return `${idx >= 0 ? trimmed.slice(0, idx) : ""}/${name}`;
}

/** Send an optionally-signed request; signs iff a secret is set and there's a body
 *  (mirrors the Python `_request`). `signPath` binds the signature to a path;
 *  defaults to the URL's path. */
async function signedRequest(
  url: string,
  method: string,
  secret: string,
  payload: unknown,
  signPath?: string,
): Promise<[number, any]> {
  warnInsecureTransport(url, Boolean(secret));
  const hasBody = payload !== undefined && payload !== null;
  const body = hasBody ? Buffer.from(JSON.stringify(payload)) : Buffer.alloc(0);
  let path = signPath;
  if (path === undefined) {
    try {
      path = new URL(url).pathname || "/";
    } catch {
      path = "/";
    }
  }
  const headers: Record<string, string> = {};
  if (hasBody) headers["Content-Type"] = "application/json";
  if (secret && hasBody) {
    const nonce = `cli_${Date.now()}`;
    const ts = rfc3339();
    headers[TIMESTAMP_HEADER] = ts;
    headers[REQUEST_ID_HEADER] = nonce;
    headers[SIGNATURE_HEADER] = `${SIGNATURE_VERSION}=${computeSignature(secret, ts, nonce, method, path, body)}`;
    headers["Authorization"] = "Bearer " + secret;
  }
  try {
    const r = await fetch(url, {
      method,
      headers,
      body: hasBody ? body : undefined,
      signal: AbortSignal.timeout(HTTP_TIMEOUT_MS),
    });
    const text = await r.text();
    return [r.status, text ? JSON.parse(text) : {}];
  } catch (e) {
    return [0, { error: netErr(e) }];
  }
}

/** (view, legal, isLegalMove) for a probe turn — mirrors Python `_synthetic_turn`. */
function syntheticTurn(game: string): [Record<string, unknown>, unknown[], (m: any) => boolean] {
  if (game === "monopoly") {
    const view = {
      game: "monopoly", match_id: "validate", seat: 0, phase: "roll",
      legal_actions: ["roll", "end_turn"], state: { players: [], phase: "roll" },
    };
    return [view, ["roll", "end_turn"], (m) => Boolean(m) && ["roll", "end_turn"].includes(m.action)];
  }
  if (game === "mafia") {
    const view = {
      game: "mafia", match_id: "validate", your_seat: 1, your_role: "Villager", day: 1,
      phase: "voting", alive: { "1": true, "2": true, "3": true }, legal: ["vote"], public: [], private: [],
    };
    return [view, ["vote"], (m) => Boolean(m) && m.action === "vote"];
  }
  const view = {
    game: "goofspiel", match_id: "validate", seat: 0, round: 0, current_prize: 5,
    prize_pool: 5, your_hand: [1, 2, 3, 4, 5], scores: [0, 0], legal_actions: [1, 2, 3, 4, 5],
  };
  return [view, [1, 2, 3, 4, 5], (m) => Boolean(m) && [1, 2, 3, 4, 5].includes(m.card)];
}

/** Lifecycle notification probes — mirrors Python `_lifecycle_probes`. */
function lifecycleProbes(game: string): [string, Record<string, unknown>][] {
  return [
    ["initialize", { protocol: "1.0", match_id: "validate", game, seat: 0, players: 2 }],
    ["event", { protocol: "1.0", match_id: "validate", game, seq: 1, type: "probe" }],
    ["game-end", { protocol: "1.0", match_id: "validate", game, result: {} }],
  ];
}

async function cmdValidate(a: Args): Promise<number> {
  const url = str(a, "url");
  if (!url) {
    console.error(`${BAD} usage: pyyol validate --url <endpoint> [--secret S] [--game G]`);
    return 2;
  }
  const secret = str(a, "secret");
  if (secret) warnArgvSecret();
  const game = str(a, "game") || "goofspiel";
  const checks: [string, boolean, string][] = [];

  // 1. health (unsigned GET on the sibling)
  {
    const [st, body] = await signedRequest(sibling(url, "health"), "GET", "", null);
    const healthy = st === 200 && String(body.status ?? "").toLowerCase() === "healthy";
    checks.push(["health", healthy, `${st} ${body.status ?? ""}`]);
  }
  // 2. handshake (signed POST)
  {
    const [st, body] = await signedRequest(sibling(url, "handshake"), "POST", secret, {
      platform: "agent-arena", protocol: "1.0",
    });
    const acc = st === 200 && Boolean(body.accepted);
    const games = (body.supportedGames ?? []).join(",");
    checks.push(["handshake", acc, `${st} accepted=${body.accepted} games=[${games}]`]);
  }
  // 3. a real signed turn — the platform's core call.
  {
    const [view, , pick] = syntheticTurn(game);
    const [st, move] = await signedRequest(url, "POST", secret, view);
    checks.push(["turn", st === 200 && pick(move), `${st} -> ${JSON.stringify(move)}`]);
  }
  // 4. lifecycle notifications must ack 200.
  for (const [name, payload] of lifecycleProbes(game)) {
    const [st] = await signedRequest(sibling(url, name), "POST", secret, payload);
    checks.push([name, st === 200, String(st)]);
  }

  console.log(`pyyol validate — ${url}\n`);
  let allOk = true;
  for (const [name, ok, detail] of checks) {
    allOk = allOk && ok;
    console.log(`  ${ok ? OK : BAD} ${name.padEnd(12)} ${detail}`);
  }
  console.log("\n" + (allOk ? "PASS — endpoint speaks the push protocol." : "FAIL — fix the checks marked ✗ above."));
  return allOk ? 0 : 1;
}

// ── watch (spectate a match over SSE, read-only) ────────────────────────────────

/** SSE event types that end a match, so `watch` can return control. */
const TERMINAL_EVENTS = new Set(["match_finished", "victory", "game_over", "game_finished", "finished"]);

/** A short, human line for a spectator event payload (mirrors Python `_sse_summary`). */
function sseSummary(obj: any): string {
  if (!obj || typeof obj !== "object") return String(obj);
  for (const k of ["winner", "text", "action", "card", "phase", "message"]) {
    if (obj[k] !== undefined && obj[k] !== null && obj[k] !== "") return `${k}: ${obj[k]}`;
  }
  return JSON.stringify(obj).slice(0, 70);
}

async function cmdWatch(a: Args): Promise<number> {
  const c = creds.load();
  const base = httpBase(a, c);
  if (!base) {
    console.error(`${BAD} no API url — pass --api or run \`pyyol login\`.`);
    return 2;
  }
  const match = a.positionals[0];
  if (!match) {
    console.error(`${BAD} usage: pyyol watch <match_id>`);
    return 2;
  }
  const asJson = bool(a, "json");
  const emit = (kind: string, detail: string) => {
    const ts = new Date().toISOString().slice(11, 19);
    console.log(`${ts}  ${kind.padEnd(12)} ${detail}`);
  };
  const url = `${base}/v1/match/${encodeURIComponent(match)}/watch`;
  emit("match", `spectating ${match} (read-only)`);

  let res: Response;
  try {
    res = await fetch(url, { headers: { Accept: "text/event-stream" } });
  } catch (e) {
    console.error(`${BAD} watch failed: ${netErr(e)}`);
    return 1;
  }
  if (!res.ok || !res.body) {
    const t = await res.text().catch(() => "");
    console.error(`${BAD} watch failed (${res.status}): ${t}`);
    return 1;
  }

  const reader = (res.body as ReadableStream<Uint8Array>).getReader();
  process.on("SIGINT", () => {
    console.log("\nstopped watching.");
    reader.cancel().catch(() => {});
  });
  const decoder = new TextDecoder();
  let buffer = "";
  let event: string | null = null;
  let data: string[] = [];
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let idx: number;
    while ((idx = buffer.indexOf("\n")) >= 0) {
      let line = buffer.slice(0, idx);
      buffer = buffer.slice(idx + 1);
      if (line.endsWith("\r")) line = line.slice(0, -1);
      if (line === "") {
        // frame boundary
        if (data.length) {
          const payload = data.join("\n");
          let obj: any;
          try {
            obj = JSON.parse(payload);
          } catch {
            obj = { raw: payload };
          }
          const kind = event || "event";
          emit(kind, asJson ? JSON.stringify(obj) : sseSummary(obj));
          if (TERMINAL_EVENTS.has(kind)) {
            emit("game_end", "match finished");
            reader.cancel().catch(() => {});
            return 0;
          }
        }
        event = null;
        data = [];
        continue;
      }
      if (line.startsWith(":")) continue; // keepalive comment
      const ci = line.indexOf(":");
      const field = ci >= 0 ? line.slice(0, ci) : line;
      let value2 = ci >= 0 ? line.slice(ci + 1) : "";
      if (value2.startsWith(" ")) value2 = value2.slice(1);
      if (field === "event") event = value2;
      else if (field === "data") data.push(value2);
    }
  }
  return 0;
}

// ── run (connect a file-loaded agent over WSS) ──────────────────────────────────

async function cmdRun(a: Args): Promise<number> {
  const c = creds.load();
  const connectUrl = str(a, "url") || process.env.PYYOL_URL || c?.connectUrl || "";
  if (!connectUrl) {
    console.error(`${BAD} no platform URL — pass --url, set PYYOL_URL, or run \`pyyol login\``);
    return 2;
  }
  const agentId = str(a, "agent") || process.env.PYYOL_AGENT_ID || c?.agentId || "";
  const { token, usingAgentKey } = connectionToken(a, c);
  if (str(a, "token")) warnArgvSecret();
  const file = str(a, "file") || "agent.mjs";
  const varName = str(a, "var") || "agent";

  let agent;
  try {
    agent = await loadAgentFromFile(file, varName);
  } catch (e) {
    console.error(`${BAD} ${e instanceof Error ? e.message : String(e)}`);
    return 2;
  }

  const base = httpBase(a, c);
  const conn = new RuntimeConnector(agent, {
    url: connectUrl,
    agentId,
    token,
    name: agent.name,
    games: agent.supportedGames,
    onFeed: makeFeed(bool(a, "quiet")),
    // Same silent access-token refresh wiring as `dev`/`play` — only when NOT on
    // the (non-expiring) agent key.
    ...(usingAgentKey ? {} : refreshOpts(c, base)),
  });
  process.on("SIGINT", () => conn.stop());
  try {
    await conn.run();
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

// ── serve (deploy-once HTTP worker: enable auto-play + run the built-in server) ──

async function cmdServe(a: Args): Promise<number> {
  const c = creds.load();
  const api = (str(a, "api") || c?.url || "").replace(/\/$/, "");
  // Auto-play + the connection are agent-scoped, so this rides the agent key
  // (falling back to the dashboard JWT).
  const { token } = connectionToken(a, c);
  if (str(a, "token")) warnArgvSecret();
  if (!api || !token) {
    console.error(`${BAD} run \`pyyol login\` first (need the API base + token)`);
    return 2;
  }

  // Load the agent: an explicit --file wins over pyyol.toml.
  let agent;
  const file = str(a, "file");
  try {
    if (file) {
      agent = await loadAgentFromFile(file, str(a, "var") || "agent");
    } else {
      const cfg = config.load();
      if (!cfg) {
        console.error(`${BAD} no pyyol.toml here — run \`pyyol init <dir>\` first (or pass --file).`);
        return 2;
      }
      agent = await loadAgentFromConfig(cfg);
    }
  } catch (e) {
    console.error(`${BAD} could not load your agent: ${e}`);
    return 2;
  }

  const [m, games] = autoplayOpts(a, config.load());
  const bid = num(a, "bid", 0);
  const [st, resp] = await autoplaySet(api, token, true, m, bid, games);
  if (st >= 200 && st < 300) {
    const extra = m === "ranked" ? `, bid=${bid}` : "";
    console.log(`${OK} auto-play ON — mode=${m}${extra}, games=${games.length ? games.join(",") : "default"}`);
  } else {
    console.error(`${BAD} could not enable auto-play (status ${st}: ${JSON.stringify(resp)}); serving anyway`);
  }

  const port = num(a, "port", 9099);
  const host = str(a, "host") || "127.0.0.1";
  const server = agent.serve(port, host);
  console.log("serving — the platform will drive your agent as matches are paired. Ctrl-C to stop.");

  // Hold until Ctrl-C, then flip auto-play OFF so you stop being matched once you exit.
  return new Promise<number>((done) => {
    let stopping = false;
    const shutdown = async () => {
      if (stopping) return;
      stopping = true;
      console.log("\nstopping…");
      try {
        server.close();
      } catch {
        /* ignore */
      }
      await autoplaySet(api, token, false, m, bid, games);
      console.log(`${OK} auto-play OFF`);
      done(0);
    };
    process.on("SIGINT", shutdown);
    process.on("SIGTERM", shutdown);
  });
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
  publish --manifest <file>         certify your agent for ranked
  queue <game> [--tier low|mid|high | --bid N] [--list]  enter ranked matchmaking
  wallet [--json]                   your coin balance + per-agent wallets
  replay <match_id> [--game] [--json]
  profile [handle]
  leaderboard [--game G] [--developers] [--season N]
  arenas
  status [--agent A]                (advanced) is your agent connected?
  autoplay on|off [--ranked|--mode] [--bid N] [--games G,…]
  serve [--file F] [--var V] [--port P] [--host H]  enable auto-play + run the HTTP server
  run [--file F] [--var V]          (advanced) connect a file-loaded agent over WSS
  simulate [--rounds N] [--seed N] [--opponent O]   local Goofspiel match
  validate --url URL [--secret S] [--game G]        probe a hosted endpoint
  watch <match_id> [--json]         spectate a match (read-only)
  logs [--file F] [--n N]           recent local agent logs
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
    case "wallet":
      return cmdWallet(a);
    case "queue":
      return cmdQueue(a);
    case "replay":
      return cmdReplay(a);
    case "status":
      return cmdStatus(a);
    case "autoplay":
      return cmdAutoplay(a);
    case "serve":
      return cmdServe(a);
    case "run":
      return cmdRun(a);
    case "simulate":
      return cmdSimulate(a);
    case "validate":
      return cmdValidate(a);
    case "watch":
      return cmdWatch(a);
    case "logs":
      return cmdLogs(a);
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

// Run when invoked as the bin (not when imported by tests). We must resolve
// symlinks: npm installs the bin as node_modules/.bin/pyyol → ../pyyol/dist/cli.js,
// so process.argv[1] is the SYMLINK path while import.meta.url is the real module
// path. Comparing them raw (the old check) never matched under a real install, so
// the installed `pyyol` command silently did nothing. realpathSync resolves the
// shim to the real file so both sides match.
let invoked = false;
try {
  invoked = !!process.argv[1] && realpathSync(process.argv[1]) === fileURLToPath(import.meta.url);
} catch {
  invoked = false;
}
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
