// ---------------------------------------------------------------------------
// Session handling (client-safe). Holds the two credential types the Go backend
// issues at onboarding: the dashboard JWT (ScopeUser) and the agent API key
// (ScopeAgent). No password login exists — these tokens *are* the session.
//
// This module is import-safe in both client and server components because it
// never imports `next/headers`. Server components read cookies via
// `lib/session.server.ts` instead.
// ---------------------------------------------------------------------------

export const COOKIE = {
  dash: "aa_dash", // dashboard JWT (user scope) — HttpOnly (BFF); JS can't read it
  key: "aa_key", // agent API key (agent scope) — HttpOnly (BFF); JS can't read it
  agent: "aa_agent", // agent public id (readable)
  name: "aa_name", // agent display name (readable)
  claim: "aa_claim", // pending claim token during onboarding
  hasDash: "aa_has_dash", // readable marker: a dashboard session exists
  hasKey: "aa_has_key", // readable marker: an agent key exists
} as const;

// Client callers still pass `session.dashboardToken` / `session.apiKey` as the
// request credential, but the real secrets are HttpOnly and unreadable by JS. So
// getSession() hands back SENTINELS; apiRequest recognizes them and routes the
// call through the same-origin BFF (/api/be), which injects the real cookie.
export const SENTINEL_USER = "cookie:user";
export const SENTINEL_AGENT = "cookie:agent";

export interface Session {
  dashboardToken?: string;
  apiKey?: string;
  agentId?: string;
  agentName?: string;
}

const MAX_AGE = 60 * 60 * 24; // 24h, matching the dashboard token TTL

/** Parse a raw Cookie header / document.cookie string into a Session. */
export function parseSession(cookieString: string | undefined | null): Session {
  const jar: Record<string, string> = {};
  for (const part of (cookieString ?? "").split(";")) {
    const i = part.indexOf("=");
    if (i === -1) continue;
    const k = part.slice(0, i).trim();
    const v = part.slice(i + 1).trim();
    if (k) jar[k] = decodeURIComponent(v);
  }
  // Prefer a legacy readable token if present (back-compat / older sessions);
  // otherwise fall back to the sentinel driven by the readable presence marker.
  return {
    dashboardToken: jar[COOKIE.dash] || (jar[COOKIE.hasDash] ? SENTINEL_USER : undefined),
    apiKey: jar[COOKIE.key] || (jar[COOKIE.hasKey] ? SENTINEL_AGENT : undefined),
    agentId: jar[COOKIE.agent] || undefined,
    agentName: jar[COOKIE.name] || undefined,
  };
}

/** Read the current session in the browser. Returns {} on the server. */
export function getSession(): Session {
  if (typeof document === "undefined") return {};
  return parseSession(document.cookie);
}

// NOTE: these are written client-side, so they cannot be HttpOnly (that requires
// a server Set-Cookie and a matching credentials-mode fetch flow — tracked for a
// later hardening pass). We do add `Secure` on HTTPS so tokens are never sent
// over plaintext, and SameSite=Lax to blunt CSRF. The agent key's primary
// protection is revocability: rotating (incl. `onavion login`) invalidates the
// prior key immediately (see backend RotateKey).
function cookieAttrs(maxAge: number): string {
  const secure = typeof location !== "undefined" && location.protocol === "https:" ? "; secure" : "";
  return `path=/; max-age=${maxAge}; samesite=lax${secure}`;
}
function writeCookie(name: string, value: string) {
  document.cookie = `${name}=${encodeURIComponent(value)}; ${cookieAttrs(MAX_AGE)}`;
}
function deleteCookie(name: string) {
  document.cookie = `${name}=; ${cookieAttrs(0)}`;
}

/** Persist a session (browser only). Secrets (dashboard JWT, agent key) are sent
 *  to the server and stored HttpOnly via /api/auth/session; non-secret display
 *  fields are written locally. If the server route is unreachable it falls back
 *  to local (readable) cookies so login never hard-breaks. Await it before
 *  navigating so the cookies exist on the next request. */
export async function setSession(s: Session): Promise<void> {
  if (typeof document === "undefined") return;
  // Sentinels are not real tokens — never persist them (that would happen on a
  // re-save of an already-cookie-backed session).
  const dash = s.dashboardToken && s.dashboardToken !== SENTINEL_USER ? s.dashboardToken : undefined;
  const key = s.apiKey && s.apiKey !== SENTINEL_AGENT ? s.apiKey : undefined;

  if (s.agentId) writeCookie(COOKIE.agent, s.agentId);
  if (s.agentName) writeCookie(COOKIE.name, s.agentName);

  if (!dash && !key) return; // nothing secret to store

  try {
    const res = await fetch("/api/auth/session", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        dashboard_token: dash,
        api_key: key,
        agent_id: s.agentId,
        agent_name: s.agentName,
      }),
      credentials: "same-origin",
    });
    if (!res.ok) throw new Error(String(res.status));
  } catch {
    // Fallback: keep the flow working even if the BFF route isn't available.
    if (dash) writeCookie(COOKIE.dash, dash);
    if (key) writeCookie(COOKIE.key, key);
  }
}

export async function clearSession(): Promise<void> {
  if (typeof document === "undefined") return;
  try {
    await fetch("/api/auth/session", { method: "DELETE", credentials: "same-origin" });
  } catch {
    /* fall through to clearing readable cookies below */
  }
  Object.values(COOKIE).forEach(deleteCookie);
}

export function setClaim(token: string) {
  if (typeof document !== "undefined") writeCookie(COOKIE.claim, token);
}

export function getClaim(): string | undefined {
  if (typeof document === "undefined") return undefined;
  for (const part of document.cookie.split(";")) {
    const i = part.indexOf("=");
    if (i === -1) continue;
    if (part.slice(0, i).trim() === COOKIE.claim) {
      return decodeURIComponent(part.slice(i + 1).trim());
    }
  }
  return undefined;
}
