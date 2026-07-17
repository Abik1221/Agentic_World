/**
 * Local credential storage for the `pyyol` CLI (mirrors the Python SDK's ~/.pyyol
 * store). Tokens are written to a 0600 JSON file under the config dir. Override the
 * location with PYYOL_HOME (tests/CI). The token arrives via the browser login flow
 * — the developer never pastes a key during normal onboarding.
 */
import { chmodSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export interface Credentials {
  url: string; // platform API/base URL
  connectUrl: string; // WSS connect URL (derived if empty)
  agentId: string;
  accessToken: string;
  refreshToken: string;
}

export function configDir(): string {
  return process.env.PYYOL_HOME || join(homedir(), ".pyyol");
}

function credPath(): string {
  return join(configDir(), "credentials.json");
}

export function save(creds: Credentials): string {
  const d = configDir();
  mkdirSync(d, { recursive: true });
  try {
    chmodSync(d, 0o700);
  } catch {
    /* best effort */
  }
  const path = credPath();
  // mode:0o600 applies on creation (subject to umask), so the token is never in a
  // world/group-readable file; chmod afterward covers an existing file + umask.
  writeFileSync(path, JSON.stringify(creds, null, 2), { mode: 0o600 });
  try {
    chmodSync(path, 0o600);
  } catch {
    /* best effort */
  }
  return path;
}

export function load(): Credentials | null {
  const path = credPath();
  if (!existsSync(path)) return null;
  try {
    const d = JSON.parse(readFileSync(path, "utf8"));
    return {
      url: d.url ?? "",
      connectUrl: d.connectUrl ?? d.connect_url ?? "",
      agentId: d.agentId ?? d.agent_id ?? "",
      accessToken: d.accessToken ?? d.access_token ?? "",
      refreshToken: d.refreshToken ?? d.refresh_token ?? "",
    };
  } catch {
    return null;
  }
}

export function clear(): boolean {
  const path = credPath();
  if (!existsSync(path)) return false;
  try {
    rmSync(path);
    return true;
  } catch {
    return false;
  }
}
