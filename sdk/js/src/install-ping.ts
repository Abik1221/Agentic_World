// Anonymous, once-per-version SDK install ping (mirrors the Python SDK). The first
// time the CLI runs a given version, fire ONE best-effort ping so the platform can
// show adoption analytics. Anonymous ({sdk, version} only — the server resolves a
// COUNTRY from the request and never stores the IP), fire-and-forget (never blocks
// or errors the CLI), once per version (marker file), opt-out via PYYOL_NO_TELEMETRY
// or the standard DO_NOT_TRACK.
import { existsSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { configDir } from "./credentials.js";

const OFF = new Set(["", "0", "false", "no", "off"]);

function optedOut(): boolean {
  for (const key of ["PYYOL_NO_TELEMETRY", "DO_NOT_TRACK"]) {
    const v = (process.env[key] ?? "").trim().toLowerCase();
    if (v && !OFF.has(v)) return true;
  }
  return false;
}

/** Fire the install ping at most once per version. Never throws. */
export function maybeInstallPing(apiBase: string, version: string): void {
  if (!apiBase || optedOut()) return;
  const marker = join(configDir(), `.install_pinged_${version}`);
  try {
    if (existsSync(marker)) return;
    // Mark BEFORE firing: attempt at most once per version (no retry storm).
    mkdirSync(configDir(), { recursive: true });
    writeFileSync(marker, "");
  } catch {
    return;
  }
  void fetch(apiBase.replace(/\/+$/, "") + "/v1/telemetry/install", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ sdk: "js", version }),
    signal: AbortSignal.timeout(3000),
  }).catch(() => {
    /* telemetry must never surface to the CLI */
  });
}
