// Self-contained dashboard auth for Pyyol Lens: a signed, HttpOnly session
// cookie gated on an env-configured credential. No external service — the Lens
// panel authenticates for itself.
//
// Enabled when PYYOL_LENS_AUTH_PASSWORD is set. When unset, the dashboard is
// open (documented dev/behind-VPN mode). Session tokens are HMAC-SHA256 signed
// (PYYOL_LENS_SESSION_SECRET, else derived from the password so it still signs).

import crypto from "node:crypto";
import { cookies } from "next/headers";

export const SESSION_COOKIE = "pyyol_lens_session";
export const SESSION_TTL_SECONDS = 60 * 60 * 12; // 12h

function configuredPassword(): string {
  return process.env.PYYOL_LENS_AUTH_PASSWORD ?? "";
}

export function configuredUser(): string {
  return process.env.PYYOL_LENS_AUTH_USER ?? "admin";
}

/** Auth is enforced only when a password is configured. */
export function authEnabled(): boolean {
  return configuredPassword() !== "";
}

function secret(): string {
  const s = process.env.PYYOL_LENS_SESSION_SECRET;
  if (s && s.length >= 16) return s;
  // Fallback so cookies are always signed even without an explicit secret; it
  // rotates automatically when the password changes.
  return crypto.createHash("sha256").update("pyyol-lens|" + configuredPassword()).digest("hex");
}

/** Timing-safe string equality (hashes to a fixed length to avoid leaking length
 *  and to satisfy timingSafeEqual's equal-length requirement). */
function safeEqual(a: string, b: string): boolean {
  const ha = crypto.createHash("sha256").update(a).digest();
  const hb = crypto.createHash("sha256").update(b).digest();
  return crypto.timingSafeEqual(ha, hb);
}

export function checkCredentials(user: string, password: string): boolean {
  if (!authEnabled()) return false;
  return safeEqual(user, configuredUser()) && safeEqual(password, configuredPassword());
}

/** Sign a session token for user (base64url(payload).base64url(hmac)). */
export function signSession(user: string): string {
  const payload = JSON.stringify({ u: user, exp: Math.floor(Date.now() / 1000) + SESSION_TTL_SECONDS });
  const body = Buffer.from(payload).toString("base64url");
  const mac = crypto.createHmac("sha256", secret()).update(body).digest("base64url");
  return `${body}.${mac}`;
}

function verifySession(token: string | undefined): string | null {
  if (!token) return null;
  const dot = token.indexOf(".");
  if (dot < 0) return null;
  const body = token.slice(0, dot);
  const mac = token.slice(dot + 1);
  const expected = crypto.createHmac("sha256", secret()).update(body).digest("base64url");
  const macBuf = Buffer.from(mac);
  const expBuf = Buffer.from(expected);
  if (macBuf.length !== expBuf.length || !crypto.timingSafeEqual(macBuf, expBuf)) return null;
  try {
    const p = JSON.parse(Buffer.from(body, "base64url").toString()) as { u?: unknown; exp?: unknown };
    if (typeof p.exp !== "number" || p.exp < Math.floor(Date.now() / 1000)) return null;
    return typeof p.u === "string" ? p.u : null;
  } catch {
    return null;
  }
}

/** The authenticated user for the current request, or null. Reads the session
 *  cookie server-side. When auth is disabled, returns null (callers treat
 *  disabled as "no gate"). */
export async function currentUser(): Promise<string | null> {
  if (!authEnabled()) return null;
  const store = await cookies();
  return verifySession(store.get(SESSION_COOKIE)?.value);
}

/** True when the request is allowed to see data: auth off, or a valid session. */
export async function isAuthorized(): Promise<boolean> {
  if (!authEnabled()) return true;
  return (await currentUser()) !== null;
}
