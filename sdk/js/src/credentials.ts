/**
 * Local credential storage for the `pyyol` CLI (mirrors the Python SDK's
 * keyring-or-0600-file model). The access/refresh tokens go into the OS secret
 * store when one is reachable — Keychain on macOS (`security`), Secret Service on
 * Linux (`secret-tool`) — otherwise they fall back to a 0600 JSON file under the
 * config dir. Non-secret metadata (platform URL, connect URL, agent id) is always
 * kept in that file, so the CLI can show which platform you're logged into without
 * unlocking the secret store. Override the location with PYYOL_HOME (tests/CI).
 *
 * Zero runtime deps: the secret store is driven by shelling out to the platform
 * CLI via node:child_process (spawnSync, arg arrays — never a shell string, so a
 * token can't leak into a shell log). The token arrives via the browser login flow
 * — the developer never pastes a key during normal onboarding.
 */
import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const SERVICE = "pyyol";
const ACCESS_KEY = "access_token";
const REFRESH_KEY = "refresh_token";
const API_KEY = "api_key";

export interface Credentials {
  url: string; // platform API/base URL
  connectUrl: string; // WSS connect URL (derived if empty)
  agentId: string;
  accessToken: string; // dashboard JWT (short-lived; owner-scope mgmt commands)
  refreshToken: string; // rotates the dashboard JWT
  apiKey: string; // long-lived agent key — the connection credential (no expiry)
}

export function configDir(): string {
  return process.env.PYYOL_HOME || join(homedir(), ".pyyol");
}

function credPath(): string {
  return join(configDir(), "credentials.json");
}

/** A minimal OS secret-store backend. Every method is best-effort: a false/null
 *  return (tool absent, store locked, item missing) sends the caller to the file. */
interface Keychain {
  set(key: string, secret: string): boolean;
  get(key: string): string | null;
  del(key: string): boolean;
}

/** macOS Keychain via the `security` CLI. `-U` updates an existing item. The
 *  secret rides in the argv array (no shell) — not a shell string we could log. */
const macKeychain: Keychain = {
  set(key, secret) {
    const r = spawnSync(
      "security",
      ["add-generic-password", "-U", "-s", SERVICE, "-a", key, "-w", secret],
      { encoding: "utf8" },
    );
    return !r.error && r.status === 0;
  },
  get(key) {
    const r = spawnSync("security", ["find-generic-password", "-s", SERVICE, "-a", key, "-w"], {
      encoding: "utf8",
    });
    if (r.error || r.status !== 0) return null;
    return (r.stdout ?? "").replace(/\n$/, "");
  },
  del(key) {
    const r = spawnSync("security", ["delete-generic-password", "-s", SERVICE, "-a", key], {
      encoding: "utf8",
    });
    return !r.error && r.status === 0;
  },
};

/** Linux Secret Service via the `secret-tool` CLI (libsecret). The secret is fed
 *  over stdin on store so it never appears in argv/ps. */
const linuxKeychain: Keychain = {
  set(key, secret) {
    const r = spawnSync(
      "secret-tool",
      ["store", "--label=pyyol", "service", SERVICE, "key", key],
      { input: secret, encoding: "utf8" },
    );
    return !r.error && r.status === 0;
  },
  get(key) {
    const r = spawnSync("secret-tool", ["lookup", "service", SERVICE, "key", key], {
      encoding: "utf8",
    });
    if (r.error || r.status !== 0) return null;
    const v = r.stdout ?? "";
    return v.length ? v.replace(/\n$/, "") : null;
  },
  del(key) {
    const r = spawnSync("secret-tool", ["clear", "service", SERVICE, "key", key], {
      encoding: "utf8",
    });
    return !r.error && r.status === 0;
  },
};

/** The OS secret store to use, or null → 0600-file fallback (Windows, unknown
 *  platforms). Setting PYYOL_KEYCHAIN=none forces the file path — an internal seam
 *  for deterministic tests/CI, and an escape hatch for users who prefer the file. */
function keychain(): Keychain | null {
  if (process.env.PYYOL_KEYCHAIN === "none") return null;
  if (process.platform === "darwin") return macKeychain;
  if (process.platform === "linux") return linuxKeychain;
  return null;
}

export function save(creds: Credentials): "keychain" | "file" {
  const d = configDir();
  mkdirSync(d, { recursive: true });
  try {
    chmodSync(d, 0o700);
  } catch {
    /* best effort */
  }

  let backend: "keychain" | "file" = "file";
  const meta: Credentials = { ...creds };
  const kc = keychain();
  if (kc && (creds.accessToken || creds.apiKey)) {
    // All writes must land before we drop the secrets from the file, so a partial
    // failure (store locked mid-write) leaves a complete 0600 file to fall back to.
    if (
      (!creds.accessToken || kc.set(ACCESS_KEY, creds.accessToken)) &&
      (!creds.refreshToken || kc.set(REFRESH_KEY, creds.refreshToken)) &&
      (!creds.apiKey || kc.set(API_KEY, creds.apiKey))
    ) {
      meta.accessToken = "";
      meta.refreshToken = "";
      meta.apiKey = "";
      backend = "keychain";
    }
  }

  const path = credPath();
  // mode:0o600 applies on creation (subject to umask), so the token is never in a
  // world/group-readable file; chmod afterward covers an existing file + umask.
  writeFileSync(path, JSON.stringify(meta, null, 2), { mode: 0o600 });
  try {
    chmodSync(path, 0o600);
  } catch {
    /* best effort */
  }
  return backend;
}

export function load(): Credentials | null {
  const path = credPath();
  if (!existsSync(path)) return null;
  let creds: Credentials;
  try {
    const d = JSON.parse(readFileSync(path, "utf8"));
    creds = {
      url: d.url ?? "",
      connectUrl: d.connectUrl ?? d.connect_url ?? "",
      agentId: d.agentId ?? d.agent_id ?? "",
      accessToken: d.accessToken ?? d.access_token ?? "",
      refreshToken: d.refreshToken ?? d.refresh_token ?? "",
      apiKey: d.apiKey ?? d.api_key ?? "",
    };
  } catch {
    return null;
  }
  // File held only metadata (keychain-backed) → fill the secrets from the store.
  if (!creds.accessToken && !creds.apiKey) {
    const kc = keychain();
    if (kc) {
      creds.accessToken = kc.get(ACCESS_KEY) ?? "";
      creds.refreshToken = kc.get(REFRESH_KEY) ?? "";
      creds.apiKey = kc.get(API_KEY) ?? "";
    }
  }
  return creds;
}

export function clear(): boolean {
  let removed = false;
  const kc = keychain();
  if (kc) {
    if (kc.del(ACCESS_KEY)) removed = true;
    if (kc.del(REFRESH_KEY)) removed = true;
    if (kc.del(API_KEY)) removed = true;
  }
  const path = credPath();
  if (existsSync(path)) {
    try {
      rmSync(path);
      removed = true;
    } catch {
      /* best effort */
    }
  }
  return removed;
}
