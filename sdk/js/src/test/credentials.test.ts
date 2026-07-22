import assert from "node:assert/strict";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { clear, configDir, load, save, type Credentials } from "../credentials.js";

/**
 * CI has no unlocked OS keychain, so these exercise the FILE FALLBACK path
 * deterministically: PYYOL_KEYCHAIN=none forces the 0600-file backend (the same
 * seam users hit on Windows / a headless box). PYYOL_HOME isolates the store.
 */
async function withStore(fn: (home: string) => void | Promise<void>): Promise<void> {
  const home = mkdtempSync(join(tmpdir(), "pyyol-c-"));
  const origHome = process.env.PYYOL_HOME;
  const origKc = process.env.PYYOL_KEYCHAIN;
  process.env.PYYOL_HOME = home;
  process.env.PYYOL_KEYCHAIN = "none";
  try {
    await fn(home);
  } finally {
    if (origHome === undefined) delete process.env.PYYOL_HOME;
    else process.env.PYYOL_HOME = origHome;
    if (origKc === undefined) delete process.env.PYYOL_KEYCHAIN;
    else process.env.PYYOL_KEYCHAIN = origKc;
  }
}

const sample: Credentials = {
  url: "https://api.pyyol.com",
  connectUrl: "wss://api.pyyol.com/v1/agent/connect",
  agentId: "ag_test",
  accessToken: "acc-tok",
  refreshToken: "ref-tok",
  apiKey: "sk_arena_abc_def",
};

test("save reports the file backend and load round-trips tokens + metadata", async () => {
  await withStore(() => {
    assert.equal(save(sample), "file");
    // With no keychain, the secrets live in the 0600 file alongside the metadata.
    const raw = JSON.parse(readFileSync(join(configDir(), "credentials.json"), "utf8"));
    assert.equal(raw.accessToken, "acc-tok");
    assert.equal(raw.refreshToken, "ref-tok");
    assert.equal(raw.apiKey, "sk_arena_abc_def");
    assert.equal(raw.agentId, "ag_test");
    assert.equal(raw.url, "https://api.pyyol.com");

    assert.deepEqual(load(), sample);
  });
});

test("the long-lived agent key (apiKey) round-trips through the file fallback", async () => {
  await withStore(() => {
    // A session with ONLY the agent key (no dashboard JWT) — the persistent
    // connection credential must survive save→load on its own.
    const keyOnly: Credentials = {
      url: "https://api.pyyol.com",
      connectUrl: "",
      agentId: "ag_key",
      accessToken: "",
      refreshToken: "",
      apiKey: "sk_arena_lookup_secret",
    };
    save(keyOnly);
    const loaded = load();
    assert.equal(loaded?.apiKey, "sk_arena_lookup_secret");
    assert.equal(loaded?.accessToken, "");
  });
});

test("clear removes the store and load then returns null", async () => {
  await withStore(() => {
    save(sample);
    assert.equal(clear(), true);
    assert.equal(load(), null);
    assert.equal(clear(), false); // nothing left to remove
  });
});

test("load returns null when nothing was ever saved", async () => {
  await withStore(() => {
    assert.equal(load(), null);
  });
});
