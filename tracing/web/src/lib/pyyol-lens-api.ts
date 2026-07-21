import { headers } from "next/headers";
import { isAuthorized } from "./auth";

const EXPLICIT_API_URL = process.env.PYYOL_API_URL;
const API_URL_CANDIDATES = EXPLICIT_API_URL
  ? [EXPLICIT_API_URL]
  : ["http://localhost:3001", "http://localhost:4000"];
const QUERY_URL = process.env.PYYOL_LENS_QUERY_URL ?? "http://localhost:8082";
const CONTROL_URL = process.env.PYYOL_LENS_CONTROL_URL ?? "http://localhost:8083";
const INGEST_URL = process.env.PYYOL_LENS_INGEST_URL ?? "http://localhost:8081";
const DEFAULT_ORG_ID = process.env.PYYOL_LENS_DEFAULT_ORG_ID ?? "local-dev-org";
const DEFAULT_USER_ID = process.env.PYYOL_LENS_DEFAULT_USER_ID ?? "local-dev-user";
// Shared secret for the direct query/control APIs (X-Pyyol-Key). Must match the
// backend's QUERY_API_KEY; empty when the APIs are unsecured (localhost dev).
const QUERY_API_KEY = process.env.PYYOL_LENS_API_KEY ?? "";
const keyHeader = (): Record<string, string> => (QUERY_API_KEY ? { "X-Pyyol-Key": QUERY_API_KEY } : {});
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

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T | null> {
  try {
    const res = await fetch(url, {
      cache: "no-store",
      ...init,
    });
    if (!res.ok) return null;
    return res.json() as Promise<T>;
  } catch {
    return null;
  }
}

async function fetchViaProxy<T>(path: string, surface: "query" | "control", headers: Record<string, string>): Promise<T | null> {
  for (const base of API_URL_CANDIDATES) {
    const res = await fetchJSON<T>(`${base}/pyyol-lens/${surface}${path}`, {
      headers,
    });
    if (res !== null) return res;
  }
  return null;
}

export async function fetchQuery<T>(path: string): Promise<T | null> {
  // Defense-in-depth: never fetch telemetry for an unauthenticated request, even
  // if a page renders server-side behind the login gate.
  if (!(await isAuthorized())) return null;
  const forwarded = await buildForwardHeaders();
  const proxied = await fetchViaProxy<T>(path, "query", forwarded);
  if (proxied !== null || !ALLOW_DIRECT_FALLBACK) {
    return proxied;
  }

  return fetchJSON<T>(`${QUERY_URL}${path}`, {
    headers: {
      "x-organization-id": DEFAULT_ORG_ID,
      "x-user-id": DEFAULT_USER_ID,
      ...keyHeader(),
    },
  });
}

export async function fetchControl<T>(path: string): Promise<T | null> {
  if (!(await isAuthorized())) return null;
  const forwarded = await buildForwardHeaders();
  const proxied = await fetchViaProxy<T>(path, "control", forwarded);
  if (proxied !== null || !ALLOW_DIRECT_FALLBACK) {
    return proxied;
  }

  return fetchJSON<T>(`${CONTROL_URL}${path}`, {
    headers: {
      "x-organization-id": DEFAULT_ORG_ID,
      "x-user-id": DEFAULT_USER_ID,
      ...keyHeader(),
    },
  });
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
