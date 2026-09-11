import { headers } from "next/headers";
import { isAuthorized } from "./auth";

const QUERY_URL = process.env.PYYOL_LENS_QUERY_URL ?? "http://localhost:8082";
const CONTROL_URL = process.env.PYYOL_LENS_CONTROL_URL ?? "http://localhost:8083";
const INGEST_URL = process.env.PYYOL_LENS_INGEST_URL ?? "http://localhost:8081";
const DEFAULT_ORG_ID = process.env.PYYOL_LENS_DEFAULT_ORG_ID ?? "local-dev-org";
const DEFAULT_USER_ID = process.env.PYYOL_LENS_DEFAULT_USER_ID ?? "local-dev-user";
// Shared secret for the direct query/control APIs (X-Pyyol-Key). Must match the
// backend's QUERY_API_KEY; empty when the APIs are unsecured (localhost dev).
const QUERY_API_KEY = process.env.PYYOL_LENS_API_KEY ?? "";
const keyHeader = (): Record<string, string> => (QUERY_API_KEY ? { "X-Pyyol-Key": QUERY_API_KEY } : {});
const FETCH_TIMEOUT_MS = 8000;
const ARENA_TIMEOUT_MS = 12000;

// Optional BFF in front of query/control. Unset in production — the dashboard talks
// to the Lens APIs directly. PYYOL_API_URL is the arena (queue health), not a BFF,
// and must never be probed as /pyyol-lens/... (that hang-then-fallback made every
// page wait on a dead localhost:3001/4000).
const LENS_BFF_URL = process.env.PYYOL_LENS_BFF_URL;
const ALLOW_DIRECT_FALLBACK = process.env.PYYOL_LENS_ALLOW_DIRECT_FALLBACK
  ? process.env.PYYOL_LENS_ALLOW_DIRECT_FALLBACK === "true"
  : process.env.NODE_ENV !== "production";

async function buildForwardHeaders() {
  const h = await headers();
  const out: Record<string, string> = {};
  const cookie = h.get("cookie");
  const authorization = h.get("authorization");
  if (cookie) out.cookie = cookie;
  if (authorization) out.authorization = authorization;
  return out;
}

export type LensResult<T> =
  | { ok: true; data: T }
  | { ok: false; reason: "unauthorized" | "unavailable" | "not_found" };

async function fetchJSONResult<T>(url: string, init?: RequestInit): Promise<LensResult<T>> {
  try {
    const res = await fetch(url, {
      cache: "no-store",
      signal: AbortSignal.timeout(FETCH_TIMEOUT_MS),
      ...init,
    });
    if (res.status === 401 || res.status === 403) return { ok: false, reason: "unauthorized" };
    if (res.status === 404) return { ok: false, reason: "not_found" };
    if (!res.ok) return { ok: false, reason: "unavailable" };
    return { ok: true, data: (await res.json()) as T };
  } catch {
    return { ok: false, reason: "unavailable" };
  }
}

async function fetchLens<T>(path: string, surface: "query" | "control"): Promise<LensResult<T>> {
  if (!(await isAuthorized())) return { ok: false, reason: "unauthorized" };
  const directHeaders = {
    "x-organization-id": DEFAULT_ORG_ID,
    "x-user-id": DEFAULT_USER_ID,
    ...keyHeader(),
  };
  const directBase = surface === "query" ? QUERY_URL : CONTROL_URL;

  if (LENS_BFF_URL) {
    const proxied = await fetchJSONResult<T>(`${LENS_BFF_URL.replace(/\/$/, "")}/pyyol-lens/${surface}${path}`, {
      headers: await buildForwardHeaders(),
    });
    if (proxied.ok || !ALLOW_DIRECT_FALLBACK) return proxied;
  }

  return fetchJSONResult<T>(`${directBase}${path}`, { headers: directHeaders });
}

export async function fetchQueryResult<T>(path: string): Promise<LensResult<T>> {
  return fetchLens<T>(path, "query");
}

export async function fetchQuery<T>(path: string): Promise<T | null> {
  const result = await fetchQueryResult<T>(path);
  return result.ok ? result.data : null;
}

export async function fetchControlResult<T>(path: string): Promise<LensResult<T>> {
  return fetchLens<T>(path, "control");
}

export async function fetchControl<T>(path: string): Promise<T | null> {
  const result = await fetchControlResult<T>(path);
  return result.ok ? result.data : null;
}

export type ServiceHealth = {
  query: boolean;
  ingest: boolean;
  control: boolean;
};

async function ping(base: string): Promise<boolean> {
  try {
    const res = await fetch(`${base}/health`, {
      cache: "no-store",
      signal: AbortSignal.timeout(1500),
    });
    return res.ok;
  } catch {
    return false;
  }
}

/** Liveness of the three backend services, for the topbar status chips. Checked
 *  server-side (internal URLs); a slow/dead service reports down within ~1.5s. */
export async function fetchServiceHealth(): Promise<ServiceHealth> {
  const [query, ingest, control] = await Promise.all([
    ping(QUERY_URL),
    ping(INGEST_URL),
    ping(CONTROL_URL),
  ]);
  return { query, ingest, control };
}

export type TraceSummary = {
  trace_id: string;
  request_id: string;
  status: string;
  organization_id: string;
  project_id: string;
  environment: string;
  user_id: string;
  started_at: string;
  ended_at: string;
  latency_ms: number;
  event_count: number;
  total_tokens: number;
  total_cost: number;
  error_type: string;
  error_message: string;
  model: string;
  tool_name: string;
};

export type SpanNode = {
  span_id: string;
  trace_id: string;
  parent_span_id: string;
  span_type: string;
  step_name: string;
  status: string;
  started_at: string;
  ended_at: string;
  latency_ms: number;
  provider: string;
  model: string;
  tool_name: string;
  total_tokens: number;
  estimated_cost: number;
  error_type: string;
  error_message: string;
  /** Rolled-up fields from spans projection (Phase 10). */
  event_type?: string;
  task_kind?: string;
  archetype?: string;
  scope?: string;
  subagent_id?: string;
  artifact_ids_in?: string[];
  artifact_ids_out?: string[];
  children?: SpanNode[];
};

export type BenchmarkStat = {
  agent_id: string;
  agent_version?: string;
  provider?: string;
  model?: string;
  game: string;
  matches: number;
  decisions: number;
  legal: number;
  illegal: number;
  timeouts: number;
  transport_errors: number;
  disconnects: number;
  errors: number;
  fallbacks: number;
  wins: number;
  losses: number;
  draws: number;
  legal_rate: number;
  fallback_rate: number;
  timeout_rate: number;
  win_rate: number;
  win_rate_lb: number;
  avg_latency_ms: number;
  model_calls: number;
  total_tokens: number;
  estimated_cost: number;
  tokens_per_match: number;
  cost_per_match: number;
  cost_per_win: number;
  avg_model_latency_ms: number;
};

export type ProviderBenchmark = {
  provider: string;
  model: string;
  game: string;
  agents: number;
  matches: number;
  decisions: number;
  win_rate: number;
  legal_rate: number;
  fallback_rate: number;
  timeout_rate: number;
  avg_latency_ms: number;
};

export type EventRow = {
  event_id: string;
  trace_id: string;
  span_id: string;
  event_type: string;
  event_time: string;
  status: string;
  model?: string;
  tool_name?: string;
  provider?: string;
  latency_ms?: number;
  step_name?: string;
  span_type?: string;
  task_kind?: string;
  parent_span_id?: string;
  error_message?: string;
  payload_json?: Record<string, unknown>;
  estimated_cost?: number;
  total_tokens?: number;
  prompt_tokens?: number;
  completion_tokens?: number;
};

// ── the arena's own admin API ─────────────────────────────────────────────────

/**
 * fetchArena calls the ARENA backend directly (not the Lens query/control APIs).
 *
 * Every other section here reads Lens's own ClickHouse. The queue funnel lives in the arena's
 * Postgres, and deliberately stays there: copying it into Lens would give two stores that can
 * disagree about "how many agents never matched", and a metric with two answers is worse than
 * a metric fetched over HTTP.
 *
 * Auth is FORWARDED, never minted here. The arena enforces RequirePlatformOrAdmin on these
 * routes, so this passes the caller's cookie/Authorization through and lets the arena decide.
 * Lens holding an admin credential of its own would turn a telemetry UI into a second place
 * platform authority can leak from.
 *
 * Returns a discriminated result rather than null: "not configured", "not authorised" and
 * "no data" have to render differently. An empty funnel shown for an auth failure reads as
 * "the queue is healthy", which is the one wrong conclusion this page must never invite.
 */
export type ArenaResult<T> =
  | { ok: true; data: T }
  | { ok: false; reason: "unconfigured" | "unauthorized" | "unreachable" };

export async function fetchArena<T>(path: string): Promise<ArenaResult<T>> {
  if (!(await isAuthorized())) return { ok: false, reason: "unauthorized" };
  const base = process.env.PYYOL_API_URL;
  // No explicit base ⇒ unconfigured, NOT a localhost guess. Silently probing localhost in a
  // deployed environment is how a dashboard ends up showing a developer laptop's numbers.
  if (!base) return { ok: false, reason: "unconfigured" };

  const forwarded = await buildForwardHeaders();
  try {
    const res = await fetch(`${base.replace(/\/$/, "")}${path}`, {
      cache: "no-store",
      headers: forwarded,
      signal: AbortSignal.timeout(ARENA_TIMEOUT_MS),
    });
    if (res.status === 401 || res.status === 403) return { ok: false, reason: "unauthorized" };
    if (!res.ok) return { ok: false, reason: "unreachable" };
    return { ok: true, data: (await res.json()) as T };
  } catch {
    return { ok: false, reason: "unreachable" };
  }
}

/** Public arena documents (replay, roster) — no admin session required. */
export async function fetchArenaPublic<T>(path: string): Promise<ArenaResult<T>> {
  if (!(await isAuthorized())) return { ok: false, reason: "unauthorized" };
  const base = process.env.PYYOL_API_URL;
  if (!base) return { ok: false, reason: "unconfigured" };
  try {
    const res = await fetch(`${base.replace(/\/$/, "")}${path}`, {
      cache: "no-store",
      signal: AbortSignal.timeout(ARENA_TIMEOUT_MS),
    });
    if (res.status === 401 || res.status === 403) return { ok: false, reason: "unauthorized" };
    if (!res.ok) return { ok: false, reason: "unreachable" };
    return { ok: true, data: (await res.json()) as T };
  } catch {
    return { ok: false, reason: "unreachable" };
  }
}

export type MatchListItem = {
  match_id: string;
  game: string;
  status: string;
  started_at: string;
  ended_at?: string;
  event_count: number;
  agents: string[];
  winner_agent?: string;
  bid: number;
  tokens: number;
  cost_usd: number;
  decisions: number;
  chat_lines: number;
};

export type MatchListResponse = {
  matches: MatchListItem[];
  total: number;
  limit: number;
  offset: number;
};

export type MatchCostAgent = {
  agent_id: string;
  provider: string;
  model: string;
  meter_source?: string;
  calls: number;
  prompt_tokens: number;
  completion_tokens: number;
  reasoning_tokens: number;
  total_tokens: number;
  cost_usd: number;
  avg_latency_ms: number;
};

export type MatchCostResponse = {
  match_id: string;
  agents: MatchCostAgent[];
  total_tokens: number;
  total_cost_usd: number;
};

export type MatchLogEntry = {
  event_id: string;
  type: string;
  at: string;
  status: string;
  agent_id?: string;
  game?: string;
  provider?: string;
  model?: string;
  tokens?: number;
  cost_usd?: number;
  latency_ms?: number;
  error?: string;
  detail?: Record<string, unknown>;
};

export type MatchMoneySeat = {
  agent_id: string;
  seat: number;
  score: number;
  coins_delta: number;
};

export type MatchMoneyResponse = {
  match_id: string;
  game?: string;
  available: boolean;
  settled?: boolean;
  bid?: number;
  rake_pct?: number;
  pool?: number;
  rake_coins?: number;
  winner_agent?: string;
  seats?: MatchMoneySeat[];
};

export type AppealRow = {
  dispute_id: string;
  match_id: string;
  agent_id?: string;
  kind: string;
  status: string;
  at: string;
  detail?: Record<string, unknown>;
  error?: string;
};

export type AppealListResponse = {
  appeals: AppealRow[];
  total: number;
  limit: number;
  offset: number;
};

export type ArenaReplay = {
  match_id: string;
  status: string;
  replay_hash?: string;
  events?: { type?: string; seq?: number; [k: string]: unknown }[];
  roster?: { seat: number; agent_id: string; name?: string; owner?: string }[];
  timing?: { seq: number; at: string; offset_ms: number }[];
};

export type ArenaRoster = {
  seats?: { seat: number; agent_id: string; name?: string; owner?: string }[];
  players?: number;
};
