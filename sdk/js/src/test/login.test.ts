import assert from "node:assert/strict";
import { test } from "node:test";

import { LOGIN_BAD_HTML, LOGIN_OK_HTML, runLoginFlow } from "../login.js";

test("loopback pages are branded and self-contained", () => {
  for (const html of [LOGIN_OK_HTML, LOGIN_BAD_HTML]) {
    assert.match(html, /<style>/);
    assert.match(html, /#000/);
    assert.match(html, /aria-label="pyyol"/);
    assert.match(html, /viewport/);
    assert.doesNotMatch(html, /Times/);
    for (const bad of ["http://", "https://", "<script", "<img"]) {
      assert.ok(!html.includes(bad), `loopback page must not contain ${bad}`);
    }
  }
});

test("success page tells the user to return to the terminal", () => {
  assert.match(LOGIN_OK_HTML.toLowerCase(), /terminal/);
  assert.match(LOGIN_BAD_HTML.toLowerCase(), /nothing was signed in/);
});

test("login accepts an agent key when the dashboard sends no JWT", async () => {
  const creds = await runLoginFlow({
    dashboardUrl: "https://pyyol.com",
    apiUrl: "https://api.pyyol.com",
    timeoutMs: 8_000,
    open(url) {
      const u = new URL(url);
      const cb = u.searchParams.get("callback") ?? "";
      const state = u.searchParams.get("state") ?? "";
      void fetch(`${cb}?state=${encodeURIComponent(state)}&api_key=sk_arena_only&agent_id=ag_2`);
    },
  });
  assert.equal(creds.apiKey, "sk_arena_only");
  assert.equal(creds.agentId, "ag_2");
});
