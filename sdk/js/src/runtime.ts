/**
 * The Pyyol Runtime Connector — the local-runtime transport for JS/TS.
 *
 * Your agent runs on your own machine and dials OUT over a single persistent
 * WebSocket to the platform. The platform pushes match lifecycle down that socket
 * and reads decisions back over it, so a laptop behind NAT works with zero
 * networking config — you never host an inbound endpoint. This is the Beta model.
 *
 * The connector owns registration, heartbeats, automatic reconnection with
 * exponential backoff, and request/response correlation; it dispatches frames to
 * the handlers you registered on your `Agent`. No game logic lives here.
 *
 *     const agent = new Agent({ supportedGames: ["goofspiel"], name: "OlympAI" });
 *     agent.onTurn("goofspiel", (v) => ({ round: v.round, card: Math.max(...v.legal_actions) }));
 *     await agent.run({ url: "wss://pyyol.example/v1/agent/connect", agentId: "ag_…", token: "…" });
 */
import type { Agent } from "./server.js";
import { SDK_VERSION } from "./server.js";
import { Tracer } from "./telemetry.js";

// Frame types — byte-identical to the Go gateway (internal/agentgw/frame.go).
const HELLO = "hello", REGISTERED = "registered", PONG = "pong";
const INITIALIZE = "initialize", TURN = "turn", EVENT = "event", GAME_END = "game_end", ERROR = "error";
const REGISTER = "register", PING = "ping", RESPONSE = "response";

export const PROTOCOL_VERSION = "1.0";

/** A minimal structural type for a WHATWG WebSocket (Node 22+ global, or `ws`). */
export interface WebSocketLike {
  send(data: string): void;
  close(code?: number, reason?: string): void;
  addEventListener(type: string, listener: (ev: any) => void): void;
  readyState: number;
}
export type WebSocketCtor = new (url: string) => WebSocketLike;

export interface RuntimeOptions {
  url: string;
  agentId?: string;
  token?: string;
  name?: string;
  games?: string[];
  version?: string;
  heartbeatMs?: number;
  reconnect?: boolean;
  maxBackoffMs?: number;
  /** Bound the opening WS handshake (ms); dead-air links hang without it. Default 10s. */
  connectTimeoutMs?: number;
  /** Inject a WebSocket implementation (defaults to globalThis.WebSocket). */
  WebSocketImpl?: WebSocketCtor;
  /** Called (once per process) with a one-line notice when the gateway reports a
   *  newer SDK is available. Defaults to console.warn. */
  onNotice?: (message: string) => void;
  /** Optional live feed of match lifecycle for a CLI/console. kind is one of
   *  "connected" | "turn" | "event" | "game_end" | "reauth". No-op if unset. */
  onFeed?: (kind: string, detail: string) => void;
  /** Rotating refresh token. When set (with apiUrl), a rejected register spends it
   *  for a fresh access token and reconnects — so long-running agents stay authed
   *  past the short access-token TTL. */
  refreshToken?: string;
  /** API base URL for POST /v1/auth/refresh (e.g. https://api.pyyol.com). */
  apiUrl?: string;
  /** Persist a rotated (access, refresh) pair, e.g. back to the credentials file. */
  onTokens?: (access: string, refresh: string) => void | Promise<void>;
  /** Inject the refresh HTTP call (tests). Defaults to a fetch of /v1/auth/refresh. */
  refreshHttp?: (apiUrl: string, refreshToken: string) => Promise<{ access: string; refresh: string } | null>;
}

/** POST {apiUrl}/v1/auth/refresh {refresh_token} → {access, refresh} or null.
 *  A non-200 (refresh token expired/revoked) → null → terminal (must re-login). */
async function defaultRefreshHttp(
  apiUrl: string,
  refreshToken: string,
): Promise<{ access: string; refresh: string } | null> {
  try {
    const res = await fetch(apiUrl.replace(/\/$/, "") + "/v1/auth/refresh", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ refresh_token: refreshToken }),
    });
    if (!res.ok) return null;
    const j = (await res.json().catch(() => null)) as { dashboard_token?: string; refresh_token?: string } | null;
    if (!j?.dashboard_token) return null;
    return { access: j.dashboard_token, refresh: j.refresh_token ?? "" };
  } catch {
    return null;
  }
}

function summarizeMove(game: string, move: unknown): string {
  if (!move || typeof move !== "object") return String(move);
  const m = move as Record<string, unknown>;
  if ("card" in m) return `card ${m.card}${m.round !== undefined ? ` (round ${m.round})` : ""}`;
  if ("action" in m) return `${m.action}${m.target !== undefined ? ` → ${m.target}` : ""}`;
  return JSON.stringify(m);
}

function summarizeResult(result: unknown): string {
  if (!result || typeof result !== "object") return "game finished";
  const outer = result as Record<string, unknown>;
  // The gateway may nest the outcome under `result` (with a viewer-relative winner
  // label like "you"/"opponent"/"tie"); unwrap it like the Python SDK does.
  const inner = (
    outer.result && typeof outer.result === "object" ? outer.result : outer
  ) as Record<string, unknown>;
  const bits: string[] = [];
  if (inner.winner !== undefined && inner.winner !== null) bits.push(`winner: ${inner.winner}`);
  for (const k of ["coins_delta", "your_coins", "coins"]) {
    if (inner[k]) {
      bits.push(`${k}=${inner[k]}`);
      break;
    }
  }
  return "game finished" + (bits.length ? " · " + bits.join(" · ") : "");
}

/** Terminal connector failure (e.g. auth rejected) — not retried. */
export class ConnectorError extends Error {}

/** Internal: the access token was just refreshed; reconnect immediately with the
 *  new token (not a transport error, so no backoff / scary log). */
class RefreshRetry extends Error {}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** Parse a dotted version into a comparable number array; unparseable ⇒ [0] so a
 *  garbage latest_sdk never triggers a spurious nudge. */
function versionParts(v: string): number[] {
  const core = v.trim().replace(/^v/, "").split(/[-+]/)[0];
  const parts = core.split(".").map((p) => Number(p));
  return parts.some((n) => !Number.isFinite(n)) ? [0] : parts;
}

/** True if a is strictly older than b (numeric field-by-field). */
function isOlder(a: string, b: string): boolean {
  const pa = versionParts(a);
  const pb = versionParts(b);
  const n = Math.max(pa.length, pb.length);
  for (let i = 0; i < n; i++) {
    const x = pa[i] ?? 0;
    const y = pb[i] ?? 0;
    if (x !== y) return x < y;
  }
  return false;
}

export class RuntimeConnector {
  private stopped = false;
  private nudged = false; // print the "upgrade available" notice at most once
  private registered = false; // true once this session's register succeeded
  private refreshAttempts = 0; // per-connection guard against a refresh loop
  // Opt-in Pyyol Lens telemetry (no-op unless PYYOL_LENS_ENDPOINT+KEY set).
  // Correlated to the match trace so the agent's model/tool calls render with
  // the platform's authoritative gateway spans.
  private readonly tracer: Tracer;
  constructor(private agent: Agent, private opts: RuntimeOptions) {
    this.tracer = Tracer.fromEnv({ agentId: opts.agentId, service: opts.name });
  }

  /** The gateway echoes the newest published version on the registered frame.
   *  Print a one-line upgrade hint at most once (not on every reconnect). */
  private maybeNudge(latest: unknown): void {
    if (this.nudged || typeof latest !== "string" || !latest) return;
    if (isOlder(SDK_VERSION, latest)) {
      this.nudged = true;
      const notify = this.opts.onNotice ?? ((m: string) => console.warn(m));
      notify(
        `a new pyyol ${latest} is available (you have ${SDK_VERSION}) — upgrade with \`npm update pyyol\``,
      );
    }
  }

  /** Connect and serve until stopped, reconnecting with exponential backoff. */
  async run(): Promise<void> {
    let backoff = 1000;
    const maxBackoff = this.opts.maxBackoffMs ?? 30000;
    while (!this.stopped) {
      try {
        await this.session();
        backoff = 1000;
        if (this.opts.reconnect === false) return;
      } catch (e) {
        if (e instanceof RefreshRetry) {
          // Token was just refreshed — reconnect right away with the new one, no
          // backoff and no "connection lost" noise (even in one-shot mode).
          this.feed("reauth", "access token refreshed — reconnecting");
          backoff = 1000;
          continue;
        }
        if (e instanceof ConnectorError) throw e; // terminal (auth) — don't spin
        if (this.opts.reconnect === false || this.stopped) throw e;
        // Surface the retry so a bad/absent network doesn't look like a frozen
        // terminal (the reason is often "can't reach the platform").
        this.feed("reconnecting", `${(e as Error).message} — retrying in ${Math.round(backoff / 1000)}s`);
        await sleep(backoff);
        backoff = Math.min(backoff * 2, maxBackoff);
      }
    }
  }

  stop(): void {
    this.stopped = true;
    void this.tracer.close();
  }

  /** Emit a live-feed line to the CLI/console, if a sink was provided. */
  private feed(kind: string, detail: string): void {
    this.opts.onFeed?.(kind, detail);
  }

  /** Warn (once) when connecting over cleartext ws:// to a non-local host — the
   *  register token is sent in the clear (mirrors the Python connector). */
  private securityCheck(): void {
    try {
      const u = new URL(this.opts.url);
      if (u.protocol === "ws:") {
        const h = u.hostname.toLowerCase();
        if (!(h === "localhost" || h === "127.0.0.1" || h === "::1" || h.endsWith(".localhost"))) {
          // Security warning goes straight to stderr — NOT through onNotice, which is
          // reserved for the SDK upgrade nudge (a host app may route that to its UI).
          console.warn(
            `WARNING: connecting over insecure ws:// to ${h} — your token is sent in cleartext; use wss://`,
          );
        }
      }
    } catch {
      /* unparseable url — the connect below will surface it */
    }
  }

  /** One connection lifetime: resolves on a clean stop, rejects on transport loss. */
  /** Spend the refresh token for a fresh access token; returns true on success
   *  (opts.token rotated + persisted). Bounded per connection to avoid a loop. */
  private async tryRefresh(): Promise<boolean> {
    const rt = this.opts.refreshToken;
    const api = this.opts.apiUrl;
    if (!rt || !api || this.refreshAttempts >= 2) return false;
    this.refreshAttempts++;
    let pair: { access: string; refresh: string } | null;
    try {
      pair = await (this.opts.refreshHttp ?? defaultRefreshHttp)(api, rt);
    } catch {
      return false;
    }
    if (!pair?.access) return false;
    this.opts.token = pair.access;
    if (pair.refresh) this.opts.refreshToken = pair.refresh;
    if (this.opts.onTokens) {
      try {
        await this.opts.onTokens(pair.access, this.opts.refreshToken ?? "");
      } catch {
        /* persistence is best-effort */
      }
    }
    return true;
  }

  private session(): Promise<void> {
    this.securityCheck();
    this.registered = false;
    const WS = this.opts.WebSocketImpl ?? (globalThis as any).WebSocket;
    if (!WS) throw new ConnectorError("no WebSocket implementation (Node >=22 or pass WebSocketImpl)");
    let host = this.opts.url;
    try { host = new URL(this.opts.url).host; } catch { /* keep raw */ }
    this.feed("connecting", host);
    const ws: WebSocketLike = new WS(this.opts.url);

    return new Promise<void>((resolve, reject) => {
      let hb: ReturnType<typeof setInterval> | undefined;
      let settled = false;
      let opened = false;
      let queue: Promise<void> = Promise.resolve(); // serialize frame dispatch (FIFO)
      const send = (f: Record<string, unknown>) => ws.send(JSON.stringify(f));
      // Bound the OPENING handshake — the WHATWG WebSocket has no connect timeout, so
      // a dead-air link (accepts TCP, never upgrades) would hang forever. On timeout
      // we fail with a transient error → run() reconnects.
      const openTimer = setTimeout(() => {
        if (!opened) fail(new Error("connect timed out (platform unreachable)"));
      }, this.opts.connectTimeoutMs ?? 10000);
      const cleanup = () => {
        clearTimeout(openTimer);
        if (hb) clearInterval(hb);
        try { ws.close(); } catch { /* already closing */ }
      };
      const fail = (e: Error) => { if (!settled) { settled = true; cleanup(); reject(e); } };
      const done = () => { if (!settled) { settled = true; cleanup(); resolve(); } };
      ws.addEventListener("open", () => { opened = true; });

      ws.addEventListener("message", (ev: any) => {
        let frame: Record<string, any>;
        try {
          frame = JSON.parse(typeof ev.data === "string" ? ev.data : String(ev.data));
        } catch { return; }
        // Chain onto the queue so frames are handled strictly one-at-a-time, in
        // arrival order — responses can't be sent out of order under a burst.
        queue = queue.then(async () => {
          if (settled) return;
          try {
            await this.dispatch(frame, send, () => {
              hb = setInterval(() => send({ t: PING }), this.opts.heartbeatMs ?? 10000);
            });
          } catch (e) {
            fail(e instanceof Error ? e : new Error(String(e)));
          }
        });
      });
      ws.addEventListener("close", () => {
        // Drain any queued frame dispatches BEFORE settling, so a close that arrives
        // right after the last frame doesn't cut off its handling (e.g. a final turn
        // response). In one-shot mode (or after stop()) a close is a clean end;
        // otherwise it is a transport loss that run() turns into a reconnect.
        queue = queue.then(() => {
          if (this.stopped || this.opts.reconnect === false) done();
          else fail(new Error("connection closed"));
        });
      });
      ws.addEventListener("error", () => { /* close fires next; handled there */ });
    });
  }

  private async dispatch(frame: Record<string, any>, send: (f: Record<string, unknown>) => void, onRegistered: () => void): Promise<void> {
    switch (frame.t) {
      case HELLO:
        send({
          t: REGISTER, agent_id: this.opts.agentId ?? "", token: this.opts.token ?? "",
          agent_name: this.opts.name ?? "pyyol-agent", version: this.opts.version ?? "1.0.0",
          games: this.opts.games ?? [], sdk_version: SDK_VERSION, sdk_language: "js",
        });
        break;
      case REGISTERED:
        this.registered = true;
        this.refreshAttempts = 0; // a good register clears the refresh guard
        this.maybeNudge(frame.latest_sdk);
        this.feed("connected", `as ${frame.agent_id ?? this.opts.agentId ?? ""} · games=${(this.opts.games ?? []).join(",")}`);
        onRegistered();
        break;
      case ERROR:
        if (!this.registered) {
          // Register rejected — almost always an expired access token. If we hold a
          // refresh token, spend it and reconnect; only terminal when refresh fails.
          if (await this.tryRefresh()) throw new RefreshRetry();
          throw new ConnectorError(`register rejected: ${frame.error} (${frame.reason ?? ""})`);
        }
        // Mid-session gateway error — surface it, not terminal (mirrors Python).
        this.feed("error", `${frame.error} (${frame.reason ?? ""})`);
        break;
      case PING:
        send({ t: PONG, id: frame.id ?? "" });
        break;
      case PONG:
        break;
      case TURN: {
        const view = frame.payload ?? {};
        // Bracket the developer's handler in a Lens span; inside decideTurn the
        // author can reach it via pyyol.currentSpan() to record model/tool calls.
        const { status, body } = await this.tracer.runTurn(
          {
            matchId: (view.match_id as string) ?? "",
            game: (view.game as string) ?? "",
            round: Number(view.round ?? 0) || 0,
            agentId: this.opts.agentId,
          },
          () => this.agent.decideTurn(view),
        );
        if (status === 200) {
          send({ t: RESPONSE, id: frame.id ?? "", payload: body });
          this.feed("turn", summarizeMove(frame.payload?.game ?? "", body));
        } else {
          send({ t: RESPONSE, id: frame.id ?? "", error: (body as any)?.error ?? "handler_error" });
        }
        break;
      }
      case INITIALIZE: {
        const ack = await this.agent.ackInitialize(frame.payload ?? {});
        send({ t: RESPONSE, id: frame.id ?? "", payload: ack });
        break;
      }
      case EVENT:
        await this.agent.notifyEvent({
          match_id: frame.match_id ?? "", game: frame.game ?? "",
          seq: frame.seq ?? 0, type: frame.kind ?? "", payload: frame.payload,
        });
        this.feed("event", `${frame.kind ?? "event"}${frame.seq !== undefined ? ` seq=${frame.seq}` : ""}`);
        break;
      case GAME_END:
        await this.agent.notifyGameEnd({
          match_id: frame.match_id ?? "", game: frame.game ?? "", result: frame.payload,
        });
        this.feed("game_end", summarizeResult(frame.payload));
        break;
      default:
        break;
    }
  }
}
