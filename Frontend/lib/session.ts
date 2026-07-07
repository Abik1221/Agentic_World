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
  dash: "aa_dash", // dashboard JWT (user scope)
  key: "aa_key", // agent API key (agent scope)
  agent: "aa_agent", // agent public id
  name: "aa_name", // agent display name
  claim: "aa_claim", // pending claim token during onboarding
} as const;

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
  return {
    dashboardToken: jar[COOKIE.dash] || undefined,
    apiKey: jar[COOKIE.key] || undefined,
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

/** Persist a session (browser only). Only provided fields are written. */
export function setSession(s: Session) {
  if (typeof document === "undefined") return;
  if (s.dashboardToken) writeCookie(COOKIE.dash, s.dashboardToken);
  if (s.apiKey) writeCookie(COOKIE.key, s.apiKey);
  if (s.agentId) writeCookie(COOKIE.agent, s.agentId);
  if (s.agentName) writeCookie(COOKIE.name, s.agentName);
}

export function clearSession() {
  if (typeof document === "undefined") return;
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
