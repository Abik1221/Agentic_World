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
  // No refresh creds → a rejected register is terminal (must re-login).
  const conn = new RuntimeConnector(goofAgent(), {
    url: "ws://x", token: "bad", reconnect: false,
    WebSocketImpl: class extends FakeWS {
      constructor(_u: string) { super([{ t: "hello" }, { t: "error", error: "unauthorized", reason: "nope" }]); }
    } as any,
  });
  await assert.rejects(conn.run(), (e) => e instanceof ConnectorError);
});

test("expired access token refreshes and reconnects with the new token", async () => {
  let calls = 0;
  let ws2!: FakeWS;
  const persisted: { access?: string; refresh?: string } = {};
  // Declared and assigned together: the onFeed callback below captures `conn` lazily, so it
  // resolves after this statement completes and needs no forward declaration.
  const conn = new RuntimeConnector(goofAgent(), {
    url: "ws://x", agentId: "ag", token: "expired",
    refreshToken: "old-rt", apiUrl: "http://api",
    refreshHttp: async (api, rt) => {
      assert.equal(api, "http://api");
      assert.equal(rt, "old-rt");
      return { access: "new-access", refresh: "new-rt" };
    },
    onTokens: (a, r) => { persisted.access = a; persisted.refresh = r; },
    // Stop once the refreshed session registers, so run() returns.
    onFeed: (kind) => { if (kind === "connected") conn.stop(); },
    WebSocketImpl: class extends FakeWS {
      constructor(_u: string) {
        calls++;
        super(calls === 1
          ? [{ t: "hello" }, { t: "error", error: "unauthorized", reason: "token expired" }]
          : [{ t: "hello" }, { t: "registered", agent_id: "ag" }]);
        if (calls === 2) ws2 = this;
      }
    } as any,
  });
  await conn.run();
  assert.equal(persisted.access, "new-access"); // rotated pair persisted
  assert.equal(persisted.refresh, "new-rt");
  // The second (post-refresh) session registered with the refreshed access token.
  assert.equal(ws2.sent.find((f) => f.t === "register")!.token, "new-access");
});

test("dead refresh token is terminal", async () => {
  const conn = new RuntimeConnector(goofAgent(), {
    url: "ws://x", token: "expired", reconnect: false,
    refreshToken: "old-rt", apiUrl: "http://api",
    refreshHttp: async () => null, // refresh token expired/revoked
    WebSocketImpl: class extends FakeWS {
      constructor(_u: string) { super([{ t: "hello" }, { t: "error", error: "unauthorized" }]); }
    } as any,
  });
  await assert.rejects(conn.run(), (e) => e instanceof ConnectorError);
});

test("register sends sdk_language and a newer latest_sdk triggers one nudge", async () => {
  const notices: string[] = [];
  let ws!: FakeWS;
  const conn = new RuntimeConnector(goofAgent(), {
    url: "ws://x", agentId: "ag", token: "s", reconnect: false,
    onNotice: (m) => notices.push(m),
    WebSocketImpl: class extends FakeWS {
      constructor(_u: string) {
        super([{ t: "hello" }, { t: "registered", agent_id: "ag", latest_sdk: "9.9.9" }]);
        ws = this;
      }
    } as any,
  });
  await conn.run();
  assert.equal(ws.sent.find((f) => f.t === "register").sdk_language, "js");
  assert.equal(notices.length, 1);
  assert.match(notices[0], /9\.9\.9/);
});

test("liveness watchdog treats a silent (half-open) link as transport loss", async () => {
  // A socket that opens then goes completely silent — no frames, no close. Without
  // the watchdog this hangs forever; with it, the connector fails the session so
  // run() would reconnect (here reconnect:false → the run rejects transiently).
  class SilentWS implements WebSocketLike {
    readyState = 1;
    private listeners: Record<string, ((ev: any) => void)[]> = {};
    constructor(_u: string) {
      queueMicrotask(() => this.emit("open", {})); // opens, then eternal silence
    }
    send() { /* swallow the register frame */ }
    close() { this.readyState = 3; }
    addEventListener(type: string, l: (ev: any) => void) { (this.listeners[type] ??= []).push(l); }
    private emit(type: string, ev: any) { for (const fn of this.listeners[type] ?? []) fn(ev); }
  }
  const conn = new RuntimeConnector(goofAgent(), {
    url: "ws://x", reconnect: false, livenessTimeoutMs: 20,
    WebSocketImpl: SilentWS as any,
  });
  // Rejects (transient Error, NOT a terminal ConnectorError) rather than hanging.
  await assert.rejects(conn.run(), (e: any) => e instanceof Error && !(e instanceof ConnectorError) && /liveness/.test(e.message));
});

test("an older-or-equal latest_sdk triggers no nudge", async () => {
  const notices: string[] = [];
  const conn = new RuntimeConnector(goofAgent(), {
    url: "ws://x", reconnect: false,
    onNotice: (m) => notices.push(m),
    WebSocketImpl: class extends FakeWS {
      constructor(_u: string) { super([{ t: "hello" }, { t: "registered", latest_sdk: "0.0.1" }]); }
    } as any,
  });
  await conn.run();
  assert.equal(notices.length, 0);
});
