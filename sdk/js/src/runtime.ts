/**
 * The Onavion Runtime Connector — the local-runtime transport for JS/TS.
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
 *     await agent.run({ url: "wss://onavion.example/v1/agent/connect", agentId: "ag_…", token: "…" });
 */
import type { Agent } from "./server.js";
import { SDK_VERSION } from "./server.js";

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
  /** Inject a WebSocket implementation (defaults to globalThis.WebSocket). */
  WebSocketImpl?: WebSocketCtor;
}

/** Terminal connector failure (e.g. auth rejected) — not retried. */
export class ConnectorError extends Error {}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export class RuntimeConnector {
  private stopped = false;
  constructor(private agent: Agent, private opts: RuntimeOptions) {}

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
        if (e instanceof ConnectorError) throw e; // terminal (auth) — don't spin
        if (this.opts.reconnect === false || this.stopped) throw e;
        await sleep(backoff);
        backoff = Math.min(backoff * 2, maxBackoff);
      }
    }
  }

  stop(): void {
    this.stopped = true;
  }

  /** One connection lifetime: resolves on a clean stop, rejects on transport loss. */
  private session(): Promise<void> {
    const WS = this.opts.WebSocketImpl ?? (globalThis as any).WebSocket;
    if (!WS) throw new ConnectorError("no WebSocket implementation (Node >=22 or pass WebSocketImpl)");
    const ws: WebSocketLike = new WS(this.opts.url);

    return new Promise<void>((resolve, reject) => {
      let hb: ReturnType<typeof setInterval> | undefined;
      let settled = false;
      const send = (f: Record<string, unknown>) => ws.send(JSON.stringify(f));
      const cleanup = () => {
        if (hb) clearInterval(hb);
        try { ws.close(); } catch { /* already closing */ }
      };
      const fail = (e: Error) => { if (!settled) { settled = true; cleanup(); reject(e); } };
      const done = () => { if (!settled) { settled = true; cleanup(); resolve(); } };

      ws.addEventListener("message", async (ev: any) => {
        let frame: Record<string, any>;
        try {
          frame = JSON.parse(typeof ev.data === "string" ? ev.data : String(ev.data));
        } catch { return; }
        try {
          await this.dispatch(frame, send, () => {
            hb = setInterval(() => send({ t: PING }), this.opts.heartbeatMs ?? 10000);
          });
        } catch (e) {
          fail(e instanceof Error ? e : new Error(String(e)));
        }
      });
      ws.addEventListener("close", () => {
        // In one-shot mode (or after stop()) a close is a clean end; otherwise it
        // is a transport loss that run() turns into a reconnect.
        if (this.stopped || this.opts.reconnect === false) done();
        else fail(new Error("connection closed"));
      });
      ws.addEventListener("error", () => { /* close fires next; handled there */ });
    });
  }

  private async dispatch(frame: Record<string, any>, send: (f: Record<string, unknown>) => void, onRegistered: () => void): Promise<void> {
    switch (frame.t) {
      case HELLO:
        send({
          t: REGISTER, agent_id: this.opts.agentId ?? "", token: this.opts.token ?? "",
          agent_name: this.opts.name ?? "onavion-agent", version: this.opts.version ?? "1.0.0",
          games: this.opts.games ?? [], sdk_version: SDK_VERSION,
        });
        break;
      case REGISTERED:
        onRegistered();
        break;
      case ERROR:
        throw new ConnectorError(`gateway error: ${frame.error} (${frame.reason ?? ""})`);
      case PING:
        send({ t: PONG, id: frame.id ?? "" });
        break;
      case PONG:
        break;
      case TURN: {
        const { status, body } = await this.agent.decideTurn(frame.payload ?? {});
        if (status === 200) send({ t: RESPONSE, id: frame.id ?? "", payload: body });
        else send({ t: RESPONSE, id: frame.id ?? "", error: (body as any)?.error ?? "handler_error" });
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
        break;
      case GAME_END:
        await this.agent.notifyGameEnd({
          match_id: frame.match_id ?? "", game: frame.game ?? "", result: frame.payload,
        });
        break;
      default:
        break;
    }
  }
}
