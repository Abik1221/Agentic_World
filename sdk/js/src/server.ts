/**
 * The Pyyol agent server.
 *
 * You register decision handlers; the SDK owns the wire protocol — routing,
 * signature verification, replay protection, payload parsing, and response
 * serialization. It contains no game strategy: your turn handler returns the move.
 *
 *     import { Agent } from "pyyol";
 *
 *     const agent = new Agent({ secret: process.env.PYYOL_SECRET });
 *     agent.onTurn("goofspiel", (v) => ({ round: v.round, card: Math.min(...v.legal_actions) }));
 *     agent.serve(9099);
 *
 * Routes served (base path comes from your manifest `endpoint.url`; the rest are
 * its siblings): GET /health, POST /handshake, /initialize, <endpoint> (turn),
 * /event, /game-end.
 */
import { createServer, IncomingMessage, ServerResponse } from "node:http";
import {
  EventNotification,
  GameEndNotification,
  InitializeRequest,
  Move,
  SUPPORTED_GAMES,
  TurnView,
  parseView,
} from "./models.js";
import { Headers, ReplayGuard, VerificationError, verifyRequest } from "./signing.js";

// Single source of truth: the generated version.ts (from package.json). Imported
// for local use below and re-exported so `import { SDK_VERSION } from "pyyol"` works.
import { SDK_VERSION } from "./version.js";
export { SDK_VERSION };

export interface AgentOptions {
  /** Manifest endpoint secret — the shared key the platform signs with. When set
   *  (recommended) every POST is signature-verified with replay protection. */
  secret?: string;
  supportedGames?: string[];
  name?: string;
  /** Public agent id echoed on /handshake so the platform can confirm identity. */
  agentId?: string;
  skewSeconds?: number;
  /** Force verification on/off; defaults to on iff a secret is set. */
  verify?: boolean;
}

export interface DispatchResult {
  status: number;
  body: Record<string, unknown>;
}

type TurnHandler = (view: TurnView) => Move | Promise<Move>;

export class Agent {
  readonly secret: string;
  readonly supportedGames: string[];
  readonly name: string;
  readonly agentId: string;
  private skew: number;
  private verify: boolean;
  private replay = new ReplayGuard();

  private turnHandlers = new Map<string, TurnHandler>();
  private defaultTurn?: TurnHandler;
  private initHandler?: (r: InitializeRequest) => unknown | Promise<unknown>;
  private eventHandler?: (n: EventNotification) => unknown | Promise<unknown>;
  private gameEndHandler?: (n: GameEndNotification) => unknown | Promise<unknown>;

  constructor(opts: AgentOptions = {}) {
    this.secret = opts.secret ?? "";
    this.supportedGames = opts.supportedGames ?? [...SUPPORTED_GAMES];
    this.name = opts.name ?? "pyyol-agent";
    this.agentId = opts.agentId ?? process.env.PYYOL_AGENT_ID ?? "";
    this.skew = opts.skewSeconds ?? 300;
    this.verify = opts.verify ?? Boolean(this.secret);
  }

  /** Register the per-turn decision handler. Pass a game to scope it, or omit for
   *  a catch-all used when no game-specific handler is set. */
  onTurn(game: string | TurnHandler, handler?: TurnHandler): this {
    if (typeof game === "function") this.defaultTurn = game;
    else if (handler) this.turnHandlers.set(game, handler);
    return this;
  }
  onInitialize(fn: (r: InitializeRequest) => unknown): this { this.initHandler = fn; return this; }
  onEvent(fn: (n: EventNotification) => unknown): this { this.eventHandler = fn; return this; }
  onGameEnd(fn: (n: GameEndNotification) => unknown): this { this.gameEndHandler = fn; return this; }

  /** Route one request and return `{ status, body }`. Framework-agnostic — call it
   *  from the built-in server or any web framework you already run. `path` must be
   *  the request path as received (no query). */
  async handle(method: string, path: string, headers: Headers, body: Buffer): Promise<DispatchResult> {
    method = method.toUpperCase();
    const suffix = path.replace(/\/+$/, "").split("/").pop() ?? "";

    // Health is an unauthenticated liveness probe — never signature-checked.
    if (method === "GET" && suffix === "health") {
      return { status: 200, body: { status: "healthy", agent: this.name, version: SDK_VERSION } };
    }
    if (method !== "POST") return { status: 405, body: { error: "method_not_allowed" } };

    if (this.verify) {
      try {
        verifyRequest(this.secret, headers, method, path, body, { skewSeconds: this.skew, replayGuard: this.replay });
      } catch (e) {
        const reason = e instanceof VerificationError ? e.reason : "bad_signature";
        return { status: 401, body: { error: "unauthorized", reason } };
      }
    }

    const data = loadJson(body);

    if (suffix === "handshake") {
      const body: Record<string, unknown> = {
        accepted: true,
        sdkVersion: SDK_VERSION,
        supportedGames: this.supportedGames,
      };
      if (this.agentId) {
        body.agent_id = this.agentId;
        body.agentId = this.agentId;
      }
      const challenge = typeof data?.challenge === "string" ? data.challenge : "";
      if (challenge) body.challenge = challenge;
      return { status: 200, body };
    }
    if (suffix === "initialize") {
      const ack = this.initHandler ? await this.initHandler(data as unknown as InitializeRequest) : undefined;
      if (ack && typeof ack === "object") return { status: 200, body: ack as Record<string, unknown> };
      return { status: 200, body: { ready: true, display_name: this.name } };
    }
    if (suffix === "event") {
      if (this.eventHandler) await this.eventHandler(data as unknown as EventNotification);
      return { status: 200, body: { ok: true } };
    }
    if (suffix === "game-end") {
      if (this.gameEndHandler) await this.gameEndHandler(data as unknown as GameEndNotification);
      return { status: 200, body: { ok: true } };
    }
    return this.handleTurn(data);
  }

  private async handleTurn(data: Record<string, any>): Promise<DispatchResult> {
    const game = typeof data.game === "string" ? data.game : "";
    const handler = this.turnHandlers.get(game) ?? this.defaultTurn;
    if (!handler) return { status: 501, body: { error: "no_turn_handler", game } };
    try {
      const move = await handler(parseView(data));
      return { status: 200, body: (move as Record<string, unknown>) ?? {} };
    } catch (e) {
      // Do NOT swallow: a crashing step() is the #1 "why doesn't my agent work"
      // trap. Log the real error + stack (so `pyyol dev` shows it) and return the
      // message so the runtime can surface it in the feed. The engine still applies
      // its deterministic fallback for this turn, so the match isn't wedged.
      const msg = e instanceof Error ? e.message : String(e);
      console.error(`${game} step() threw: ${msg}`);
      if (e instanceof Error && e.stack) console.error(e.stack);
      return { status: 500, body: { error: "handler_error", message: msg } };
    }
  }

  // --- shared handler invocation (used by both the HTTP path and the socket
  // RuntimeConnector, so both transports run identical decision logic) ---

  /** Run the turn handler for a raw view dict; returns `{status, body}`. */
  decideTurn(view: Record<string, unknown>): Promise<DispatchResult> {
    return this.handleTurn(view);
  }
  /** Run the initialize handler and return the ack object. */
  async ackInitialize(data: Record<string, unknown>): Promise<Record<string, unknown>> {
    const ack = this.initHandler ? await this.initHandler(data as unknown as InitializeRequest) : undefined;
    return ack && typeof ack === "object" ? (ack as Record<string, unknown>) : { ready: true, display_name: this.name };
  }
  async notifyEvent(data: Record<string, unknown>): Promise<void> {
    if (this.eventHandler) await this.eventHandler(data as unknown as EventNotification);
  }
  async notifyGameEnd(data: Record<string, unknown>): Promise<void> {
    if (this.gameEndHandler) await this.gameEndHandler(data as unknown as GameEndNotification);
  }

  /** Connect to the platform and serve matches over an outbound WebSocket — the
   *  Beta local-runtime path (no inbound endpoint; works behind NAT). Blocks
   *  until stopped; reconnects automatically. */
  async run(opts: import("./runtime.js").RuntimeOptions): Promise<void> {
    const { RuntimeConnector } = await import("./runtime.js");
    await new RuntimeConnector(this, {
      name: this.name,
      games: this.supportedGames,
      ...opts,
    }).run();
  }

  /** Run an HTTP server until the process exits. Zero dependencies. */
  serve(port = 9099, host = "127.0.0.1"): ReturnType<typeof createServer> {
    const server = createServer((req: IncomingMessage, res: ServerResponse) => {
      const chunks: Buffer[] = [];
      req.on("data", (c) => chunks.push(c));
      req.on("end", async () => {
        const body = Buffer.concat(chunks);
        const path = (req.url ?? "/").split("?")[0];
        const { status, body: payload } = await this.handle(req.method ?? "GET", path, req.headers, body);
        const out = Buffer.from(JSON.stringify(payload));
        res.writeHead(status, { "Content-Type": "application/json", "Content-Length": String(out.length) });
        res.end(out);
      });
    });
    server.listen(port, host, () => {
      // eslint-disable-next-line no-console
      console.log(`pyyol agent "${this.name}" listening on http://${host}:${port} (verify=${this.verify})`);
    });
    return server;
  }
}

function loadJson(body: Buffer): Record<string, unknown> {
  if (!body || body.length === 0) return {};
  try {
    const obj = JSON.parse(body.toString("utf8"));
    return obj && typeof obj === "object" ? obj : {};
  } catch {
    return {};
  }
}
