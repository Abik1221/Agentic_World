import assert from "node:assert/strict";
import { PassThrough } from "node:stream";
import { describe, it } from "node:test";
import {
  INTENT_INVITE,
  INTENT_QUEUE,
  askIntent,
  friendsUrl,
  resolveStartupIntent,
} from "../intent.js";

function source(data: string, tty = true): NodeJS.ReadableStream & { isTTY?: boolean } {
  const s = new PassThrough() as PassThrough & { isTTY?: boolean };
  s.isTTY = tty;
  queueMicrotask(() => {
    s.write(data);
    s.end();
  });
  return s;
}

function sink(tty = true): NodeJS.WritableStream & { isTTY?: boolean; chunks: string[] } {
  const chunks: string[] = [];
  const s = new PassThrough() as PassThrough & { isTTY?: boolean; chunks: string[] };
  s.isTTY = tty;
  s.chunks = chunks;
  s.on("data", (c: Buffer | string) => chunks.push(String(c)));
  return s;
}

describe("friendsUrl", () => {
  it("joins the dashboard path", () => {
    assert.equal(friendsUrl("https://pyyol.com"), "https://pyyol.com/friends");
    assert.equal(friendsUrl("https://pyyol.com/"), "https://pyyol.com/friends");
    assert.equal(friendsUrl(""), "");
  });
});

describe("askIntent", () => {
  it("returns queue without prompting when not a TTY", async () => {
    const out = sink(false);
    const got = await askIntent({ stdin: source("i\n", false), stdout: out, timeoutMs: 200 });
    assert.equal(got, INTENT_QUEUE);
    assert.equal(out.chunks.join(""), "");
  });

  it("treats i as invite", async () => {
    const got = await askIntent({ stdin: source("i\n"), stdout: sink(), timeoutMs: 500 });
    assert.equal(got, INTENT_INVITE);
  });

  it("defaults everything else to join", async () => {
    for (const answer of ["j\n", "\n", "x\n"]) {
      const got = await askIntent({ stdin: source(answer), stdout: sink(), timeoutMs: 500 });
      assert.equal(got, INTENT_QUEUE, `answer ${JSON.stringify(answer)}`);
    }
  });

  it("defaults to join on timeout", async () => {
    const stdin = new PassThrough() as PassThrough & { isTTY?: boolean };
    stdin.isTTY = true;
    const out = sink();
    const got = await askIntent({ stdin, stdout: out, timeoutMs: 50 });
    assert.equal(got, INTENT_QUEUE);
    assert.match(out.chunks.join(""), /joining a game/);
  });
});

describe("resolveStartupIntent", () => {
  it("lets ranked and --queue win over invite", async () => {
    assert.equal(await resolveStartupIntent({ ranked: true, inviteFlag: true, isTty: true }), INTENT_QUEUE);
    assert.equal(await resolveStartupIntent({ queueFlag: true, inviteFlag: true, isTty: true }), INTENT_QUEUE);
  });

  it("honors --invite and --mode", async () => {
    assert.equal(await resolveStartupIntent({ inviteFlag: true, isTty: false }), INTENT_INVITE);
    assert.equal(await resolveStartupIntent({ mode: "invite", isTty: false }), INTENT_INVITE);
    assert.equal(await resolveStartupIntent({ mode: "queue", isTty: true }), INTENT_QUEUE);
  });

  it("reads PYYOL_STARTUP", async () => {
    assert.equal(await resolveStartupIntent({ env: { PYYOL_STARTUP: "invite" }, isTty: false }), INTENT_INVITE);
  });

  it("skips ask on non-TTY", async () => {
    let calls = 0;
    const got = await resolveStartupIntent({
      isTty: false,
      ask: async () => {
        calls++;
        return INTENT_INVITE;
      },
    });
    assert.equal(got, INTENT_QUEUE);
    assert.equal(calls, 0);
  });

  it("asks on TTY when unset", async () => {
    const got = await resolveStartupIntent({
      isTty: true,
      ask: async () => INTENT_INVITE,
    });
    assert.equal(got, INTENT_INVITE);
  });
});
