/**
 * The `pyyol login` browser flow (mirrors the Python SDK). Opens the dashboard's
 * `/cli-login` page and captures the issued token on a loopback callback — the
 * GitHub-CLI model. The dashboard owns the actual auth (GitHub / Google / wallet /
 * email); the CLI is provider-agnostic and just captures the returned token.
 */
import { spawn } from "node:child_process";
import { randomBytes, timingSafeEqual } from "node:crypto";
import { createServer } from "node:http";
import { hostname } from "node:os";
import type { Credentials } from "./credentials.js";

/** Constant-time string compare (length-guarded so timingSafeEqual never throws). */
function safeEqual(a: string, b: string): boolean {
  const ab = Buffer.from(a);
  const bb = Buffer.from(b);
  return ab.length === bb.length && timingSafeEqual(ab, bb);
}

/**
 * A stable name for THIS machine, used to label the agent key issued to it.
 *
 * Must be stable across logins on one machine (otherwise each login adds a key
 * instead of replacing the one it supersedes) and distinct between machines
 * (otherwise a laptop login revokes a server's key). The hostname is both; a random
 * id breaks the first property, a constant breaks the second. `.local` is stripped so
 * the label reads as the machine's name rather than its mDNS form.
 */
export function deviceLabel(): string {
  let name = "";
  try {
    name = hostname();
  } catch {
    name = "";
  }
  return name.trim().replace(/\.local$/i, "") || "pyyol cli";
}

/**
 * Loopback pages after `pyyol login`. Keep in lockstep with
 * sdk/python/pyyol/login.py. No network, no <img>, no xmlns (must not contain
 * "http://"). The lockup is the /pyyol-logo.png mark — cascade + word — inline.
 */
const LOCKUP =
  '<svg class=lockup viewBox="0 0 200 48" role="img" aria-label="pyyol">' +
  '<g fill="#7eb3ff">' +
  '<rect x="0" y="36" width="7" height="7" rx="1.5"/>' +
  '<rect x="9.2" y="36" width="7" height="7" rx="1.5"/>' +
  '<rect x="6.2" y="26.6" width="6.4" height="6.4" rx="1.4"/>' +
  '<rect x="15.2" y="26.6" width="6.4" height="6.4" rx="1.4"/>' +
  '<rect x="13" y="18.2" width="5.6" height="5.6" rx="1.3"/>' +
  '<rect x="21" y="18.2" width="5.6" height="5.6" rx="1.3"/>' +
  '<rect x="19.4" y="11.2" width="4.6" height="4.6" rx="1.15"/>' +
  '<rect x="26.2" y="11.2" width="4.6" height="4.6" rx="1.15"/>' +
  '<rect x="25.2" y="5.6" width="3.6" height="3.6" rx="1"/>' +
  '<rect x="30.6" y="5.6" width="3.6" height="3.6" rx="1"/>' +
  '<rect x="30.2" y="1.6" width="2.5" height="2.5" rx=".75"/>' +
  '<rect x="34.2" y="1.6" width="2.5" height="2.5" rx=".75"/>' +
  '<rect x="34.4" y="0" width="1.6" height="1.6" rx=".5"/>' +
  "</g>" +
  '<text x="46" y="40" fill="#e8e9ed" font-size="28" font-weight="500" ' +
  'letter-spacing="-0.04em" ' +
  "font-family=\"Space Grotesk,ui-sans-serif,system-ui,-apple-system,'Segoe UI',sans-serif\">" +
  "pyyol</text></svg>";

function loopbackPage(title: string, heading: string, copy: string): string {
  return (
    "<!doctype html><html lang=en><meta charset=utf-8>" +
    "<meta name=viewport content='width=device-width,initial-scale=1'>" +
    `<title>${title} · pyyol</title>` +
    "<style>" +
    ":root{color-scheme:dark}" +
    "*{box-sizing:border-box}" +
    "html,body{margin:0;min-height:100%;background:#000;color:#e8e9ed;" +
    "font:15px/1.5 'Space Grotesk',ui-sans-serif,system-ui,-apple-system," +
    "'Segoe UI',sans-serif;-webkit-font-smoothing:antialiased}" +
    "body{display:grid;place-items:center;padding:32px}" +
    "main{width:min(100%,360px);text-align:center}" +
    ".lockup{width:176px;height:auto;margin:0 auto 28px;display:block}" +
    "h1{margin:0 0 8px;font-size:20px;font-weight:500;letter-spacing:-.03em}" +
    "p{margin:0;color:#8b8d96;font-size:14px}" +
    "</style>" +
    `<body><main>${LOCKUP}<h1>${heading}</h1><p>${copy}</p></main>`
  );
}

/** Success page the loopback server writes after a valid callback. */
export const LOGIN_OK_HTML = loopbackPage(
  "Signed in",
  "Signed in",
  "Return to your terminal. You can close this tab.",
);
/** Failure page — wrong state or no credential. */
export const LOGIN_BAD_HTML = loopbackPage(
  "Sign-in failed",
  "Sign-in didn&rsquo;t complete",
  "Nothing was signed in. Return to your terminal and run the command again.",
);

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
      const apiKey = u.searchParams.get("api_key") ?? "";
      // Either credential is enough — same as the Python CLI. Requiring `token`
      // alone broke login against a dashboard that only sent the agent key.
      const ok = Boolean(token || apiKey) && safeEqual(u.searchParams.get("state") ?? "", state);
      res.writeHead(ok ? 200 : 400, { "Content-Type": "text/html; charset=utf-8" });
      res.end(ok ? LOGIN_OK_HTML : LOGIN_BAD_HTML);
      if (!ok) return;
      clearTimeout(timer);
      server.close();
      resolveP({
        url: apiUrl,
        connectUrl: u.searchParams.get("connect_url") || deriveConnectUrl(apiUrl),
        agentId: u.searchParams.get("agent_id") ?? "",
        accessToken: token,
        refreshToken: u.searchParams.get("refresh_token") ?? "",
        // Optional: the dashboard may hand back a long-lived agent key directly.
        // If it doesn't, `pyyol login` mints one post-auth.
        apiKey: u.searchParams.get("api_key") ?? "",
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
      // Name the key after this machine. Agent keys are one-per-label and issuing
      // replaces only the matching label (backend migration 0071), so a stable
      // per-machine name is what keeps this login from revoking another machine's or
      // a deployment's key — and it is what the owner reads in the dashboard list.
      authUrl += `&label=${encodeURIComponent(deviceLabel())}`;

      // Print the URL, then try to open it. Browser launching silently fails over
      // SSH, in WSL, and in containers, and without the link on screen the user just
      // watches a dead prompt until the timeout. Matches the Python SDK, and every
      // mature CLI, which print it for exactly this reason.
      let opened = false;
      try {
        opener(authUrl);
        opened = true;
      } catch {
        opened = false;
      }
      process.stderr.write(
        (opened ? "opening your browser to sign in…\n" : "couldn't open a browser automatically.\n") +
          `  if it didn't open, visit:\n  ${authUrl}\n\n` +
          `waiting for you to finish signing in… (up to ${Math.round(timeoutMs / 1000)}s; Ctrl-C to cancel)\n`,
      );
    });
  });
}
