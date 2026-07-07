/**
 * HMAC request signing + verification for the Pyyol push protocol.
 *
 * Reproduces EXACTLY the platform's `agentclient.SignRequest` (Go):
 *
 *     signingString = timestamp \n nonce \n METHOD \n path \n hex(sha256(body))
 *     signature     = hex(hmacSHA256(secret, signingString))
 *
 * The shared secret is the agent's endpoint token (the manifest endpoint secret).
 * An agent verifies the signature (constant-time), rejects stale timestamps
 * (clock-skew window), and rejects already-seen nonces (replay protection).
 */
import { createHash, createHmac, timingSafeEqual } from "node:crypto";

export const SIGNATURE_VERSION = "v1";
export const SIGNATURE_HEADER = "x-arena-signature";
export const TIMESTAMP_HEADER = "x-arena-timestamp";
export const REQUEST_ID_HEADER = "x-arena-request-id";

/** Default clock-skew tolerance for the timestamp, in seconds. */
export const DEFAULT_SKEW_SECONDS = 300;

export type Headers = Record<string, string | string[] | undefined>;

export type VerifyReason =
  | "missing_signature"
  | "unsupported_version"
  | "stale_timestamp"
  | "replayed_nonce"
  | "bad_signature";

export class VerificationError extends Error {
  reason: VerifyReason;
  constructor(reason: VerifyReason, message?: string) {
    super(message ?? reason);
    this.name = "VerificationError";
    this.reason = reason;
  }
}

/** Build the canonical signing string. Matches the Go builder exactly. */
export function canonicalString(
  timestamp: string,
  nonce: string,
  method: string,
  path: string,
  body: Buffer,
): string {
  const bodyHash = createHash("sha256").update(body ?? Buffer.alloc(0)).digest("hex");
  return [timestamp, nonce, method.toUpperCase(), path, bodyHash].join("\n");
}

/** Return the hex HMAC-SHA256 signature for a request. */
export function computeSignature(
  secret: string,
  timestamp: string,
  nonce: string,
  method: string,
  path: string,
  body: Buffer,
): string {
  const signing = canonicalString(timestamp, nonce, method, path, body);
  return createHmac("sha256", secret).update(signing).digest("hex");
}

/**
 * Bounded in-memory nonce cache that rejects replayed request ids. Nonces are
 * held for `ttlSeconds` (>= the skew window); the cache is size-capped so a flood
 * of unique nonces cannot exhaust memory (oldest evicted when full).
 */
export class ReplayGuard {
  private seen = new Map<string, number>();
  constructor(
    private ttlSeconds = DEFAULT_SKEW_SECONDS * 2,
    private maxSize = 50_000,
  ) {}

  /** True if `nonce` is fresh (and remembers it); false if replayed. */
  checkAndStore(nonce: string, now: number): boolean {
    this.evict(now);
    if (this.seen.has(nonce)) return false;
    if (this.seen.size >= this.maxSize) {
      const oldest = this.seen.keys().next().value; // Map preserves insertion order
      if (oldest !== undefined) this.seen.delete(oldest);
    }
    this.seen.set(nonce, now);
    return true;
  }

  private evict(now: number): void {
    const cutoff = now - this.ttlSeconds;
    for (const [k, t] of this.seen) {
      if (t < cutoff) this.seen.delete(k);
      else break; // insertion order ~ time order; the rest are newer
    }
  }
}

function header(headers: Headers, name: string): string {
  const lower = name.toLowerCase();
  const v = headers[lower] ?? headers[name];
  if (Array.isArray(v)) return v[0] ?? "";
  return v ?? "";
}

export interface VerifyOptions {
  skewSeconds?: number;
  replayGuard?: ReplayGuard;
  now?: number; // epoch seconds; defaults to Date.now()/1000
}

/**
 * Verify a signed request or throw {@link VerificationError}. `path` must be the
 * path the platform signed (the request path as received, no query). `body` is
 * the exact raw request body bytes.
 */
export function verifyRequest(
  secret: string,
  headers: Headers,
  method: string,
  path: string,
  body: Buffer,
  opts: VerifyOptions = {},
): void {
  const sigHeader = header(headers, SIGNATURE_HEADER);
  const ts = header(headers, TIMESTAMP_HEADER);
  const nonce = header(headers, REQUEST_ID_HEADER);

  if (!sigHeader || !ts || !nonce) {
    throw new VerificationError("missing_signature", "missing signature/timestamp/nonce headers");
  }

  const eq = sigHeader.indexOf("=");
  const version = eq >= 0 ? sigHeader.slice(0, eq) : "";
  const provided = eq >= 0 ? sigHeader.slice(eq + 1) : "";
  if (version !== SIGNATURE_VERSION || !provided) {
    throw new VerificationError("unsupported_version", `unsupported signature version ${version}`);
  }

  const now = opts.now ?? Date.now() / 1000;
  const skew = opts.skewSeconds ?? DEFAULT_SKEW_SECONDS;
  const tsEpoch = Date.parse(ts) / 1000;
  if (!Number.isFinite(tsEpoch) || Math.abs(now - tsEpoch) > skew) {
    throw new VerificationError("stale_timestamp", "timestamp outside the allowed skew window");
  }

  if (opts.replayGuard && !opts.replayGuard.checkAndStore(nonce, now)) {
    throw new VerificationError("replayed_nonce", "request id was already used");
  }

  const expected = computeSignature(secret, ts, nonce, method, path, body);
  const a = Buffer.from(expected);
  const b = Buffer.from(provided);
  if (a.length !== b.length || !timingSafeEqual(a, b)) {
    throw new VerificationError("bad_signature", "signature mismatch");
  }
}
