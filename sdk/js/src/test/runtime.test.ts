import { test } from "node:test";
import assert from "node:assert/strict";
import { Agent } from "../server.js";
import { RuntimeConnector, ConnectorError, type WebSocketLike } from "../runtime.js";

/** A scripted WebSocket: emits queued server frames on connect and records what
 *  the connector sends. Mirrors the WHATWG event API the connector uses. */
class FakeWS implements WebSocketLike {
  readyState = 1;
  sent: any[] = [];
  private listeners: Record<string, ((ev: any) => void)[]> = {};
  constructor(private incoming: any[]) {
    // Deliver frames + a close on the next tick, after listeners are attached.
    queueMicrotask(async () => {
      for (const f of this.incoming) {
        this.emit("message", { data: JSON.stringify(f) });
        await Promise.resolve();
      }
      this.emit("close", {});
    });
  }
  send(data: string) { this.sent.push(JSON.parse(data)); }
  close() { this.readyState = 3; }
  addEventListener(type: string, listener: (ev: any) => void) {
    (this.listeners[type] ??= []).push(listener);
  }
  private emit(type: string, ev: any) { for (const l of this.listeners[type] ?? []) l(ev); }
}

function goofAgent(): Agent {
  const a = new Agent({ supportedGames: ["goofspiel"], name: "t" });
  a.onTurn("goofspiel", (v: any) => ({ round: v.round, card: Math.max(...v.legal_actions) }));
  return a;
}

test("register handshake + turn correlation", async () => {
  let ws!: FakeWS;
  const conn = new RuntimeConnector(goofAgent(), {
    url: "ws://x", agentId: "ag", token: "s", games: ["goofspiel"], reconnect: false,
    WebSocketImpl: class extends FakeWS {
      constructor(_u: string) {
        super([
          { t: "hello", version: "1.0" },
          { t: "registered", agent_id: "ag" },
          { t: "turn", id: "r1", payload: { game: "goofspiel", round: 2, your_hand: [3, 7, 9], legal_actions: [3, 7, 9] } },
        ]);
        ws = this;
      }
    } as any,
  });
  await conn.run();
  const reg = ws.sent.find((f) => f.t === "register");
  assert.equal(reg.token, "s");
  assert.deepEqual(reg.games, ["goofspiel"]);
  const resp = ws.sent.find((f) => f.t === "response" && f.id === "r1");
  assert.deepEqual(resp.payload, { round: 2, card: 9 });
});

test("ping gets a pong", async () => {
  let ws!: FakeWS;
  const conn = new RuntimeConnector(goofAgent(), {
    url: "ws://x", reconnect: false,
    WebSocketImpl: class extends FakeWS {
      constructor(_u: string) { super([{ t: "hello" }, { t: "registered" }, { t: "ping", id: "hb1" }]); ws = this; }
    } as any,
  });
  await conn.run();
  assert.ok(ws.sent.some((f) => f.t === "pong" && f.id === "hb1"));
});

test("event + game_end callbacks fire", async () => {
  const seen: any = { events: [], end: null };
  const a = goofAgent();
  a.onEvent((n: any) => { seen.events.push([n.type, n.seq]); });
  a.onGameEnd((n: any) => { seen.end = n.result; });
  const conn = new RuntimeConnector(a, {
    url: "ws://x", reconnect: false,
    WebSocketImpl: class extends FakeWS {
      constructor(_u: string) {
        super([
          { t: "hello" }, { t: "registered" },
          { t: "event", game: "goofspiel", match_id: "m", seq: 4, kind: "round_revealed", payload: { prize: 5 } },
          { t: "game_end", game: "goofspiel", match_id: "m", payload: { winner: 0 } },
        ]);
      }
    } as any,
  });
  await conn.run();
  assert.deepEqual(seen.events, [["round_revealed", 4]]);
  assert.deepEqual(seen.end, { winner: 0 });
});

test("bad token is terminal", async () => {
  const conn = new RuntimeConnector(goofAgent(), {
    url: "ws://x", token: "bad", reconnect: false,
    WebSocketImpl: class extends FakeWS {
      constructor(_u: string) { super([{ t: "hello" }, { t: "error", error: "unauthorized", reason: "nope" }]); }
    } as any,
  });
  await assert.rejects(conn.run(), (e) => e instanceof ConnectorError);
});
