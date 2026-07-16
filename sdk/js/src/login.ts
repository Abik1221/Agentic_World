/**
 * The `pyyol login` browser flow (mirrors the Python SDK). Opens the dashboard's
 * `/cli-login` page and captures the issued token on a loopback callback — the
 * GitHub-CLI model. The dashboard owns the actual auth (GitHub / Google / wallet /
 * email); the CLI is provider-agnostic and just captures the returned token.
 */
import { spawn } from "node:child_process";
import { randomBytes, timingSafeEqual } from "node:crypto";
import { createServer } from "node:http";
import type { Credentials } from "./credentials.js";

/** Constant-time string compare (length-guarded so timingSafeEqual never throws). */
function safeEqual(a: string, b: string): boolean {
  const ab = Buffer.from(a);
  const bb = Buffer.from(b);
  return ab.length === bb.length && timingSafeEqual(ab, bb);
}

/** Derive the WSS connect URL from a platform API/base URL. */
export function deriveConnectUrl(apiUrl: string): string {
  if (!apiUrl) return "";
  try {
    const u = new URL(apiUrl);
    const scheme = u.protocol === "https:" || u.protocol === "wss:" ? "wss:" : "ws:";
    return `${scheme}//${u.host}/v1/agent/connect`;
  } catch {
    return "";
  }
}

function openBrowser(url: string): void {
  // NEVER use shell:true here — the auth URL contains `&` (…&state=…&provider=…),
  // which cmd.exe would treat as a command separator (arg/command injection via a
  // hostile --dashboard). Pass the URL as a discrete, non-shell argument.
  let cmd: string;
  let args: string[];
  if (process.platform === "win32") {
    cmd = "cmd";
    args = ["/c", "start", "", url]; // empty title arg so a quoted URL isn't taken as the title
  } else if (process.platform === "darwin") {
    cmd = "open";
    args = [url];
  } else {
    cmd = "xdg-open";
    args = [url];
  }
  try {
    const child = spawn(cmd, args, { stdio: "ignore", detached: true, shell: false });
    child.unref();
  } catch {
    /* headless: the user can open the URL manually (printed by the caller) */
  }
}

export interface LoginResult extends Credentials {
  authUrl: string;
}

/** Run the loopback browser login and resolve with captured Credentials.
 *  `open` overrides how the auth URL is opened (tests inject a fake). */
export function runLoginFlow(opts: {
  dashboardUrl: string;
  apiUrl?: string;
  provider?: string;
  timeoutMs?: number;
  open?: (url: string) => void;
}): Promise<LoginResult> {
  const state = randomBytes(16).toString("base64url");
  const apiUrl = opts.apiUrl ?? "";
  const opener = opts.open ?? openBrowser;
  const timeoutMs = opts.timeoutMs ?? 180_000;

  return new Promise<LoginResult>((resolveP, reject) => {
    const server = createServer((req, res) => {
      const u = new URL(req.url ?? "/", "http://127.0.0.1");
      if (u.pathname !== "/callback") {
        res.writeHead(404).end();
        return;
      }
      const token = u.searchParams.get("token") ?? "";
      const ok = Boolean(token) && safeEqual(u.searchParams.get("state") ?? "", state);
      res.writeHead(ok ? 200 : 400, { "Content-Type": "text/html; charset=utf-8" });
      res.end(
        ok
          ? "<!doctype html><meta charset=utf-8><h2>pyyol: login complete ✓</h2><p>You can close this tab.</p>"
          : "<!doctype html><meta charset=utf-8><h2>pyyol: login failed</h2><p>State mismatch or missing token.</p>",
      );
      if (!ok) return;
      clearTimeout(timer);
      server.close();
      resolveP({
        url: apiUrl,
        connectUrl: u.searchParams.get("connect_url") || deriveConnectUrl(apiUrl),
        agentId: u.searchParams.get("agent_id") ?? "",
        accessToken: token,
        refreshToken: u.searchParams.get("refresh_token") ?? "",
        authUrl: "",
      });
    });

    const timer = setTimeout(() => {
      server.close();
      reject(new Error("login timed out or was cancelled"));
    }, timeoutMs);

    server.listen(0, "127.0.0.1", () => {
      const addr = server.address();
      const port = typeof addr === "object" && addr ? addr.port : 0;
      const callback = `http://127.0.0.1:${port}/callback`;
      let authUrl =
        `${opts.dashboardUrl.replace(/\/$/, "")}/cli-login` +
        `?callback=${encodeURIComponent(callback)}&state=${state}`;
      if (opts.provider) authUrl += `&provider=${encodeURIComponent(opts.provider)}`;
      opener(authUrl);
    });
  });
}
