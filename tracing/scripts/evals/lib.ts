/**
 * HTTP helpers for pyyol-lens query-api (x-organization-id required).
 */
export function baseUrl(): string {
  const u = process.env.QUERY_BASE_URL || process.env.PYYOL_LENS_QUERY_URL || "http://127.0.0.1:8082";
  return u.replace(/\/$/, "");
}

export function orgId(): string {
  return process.env.PYYOL_LENS_ORG_ID || process.env.ORGANIZATION_ID || "";
}

export function runId(): string {
  return process.env.RUN_ID || process.env.TRACE_ID || "";
}

export function headers(): Record<string, string> {
  const o = orgId();
  if (!o) {
    throw new Error("set PYYOL_LENS_ORG_ID (or ORGANIZATION_ID)");
  }
  return {
    "x-organization-id": o,
    "content-type": "application/json",
  };
}

export async function getJson<T>(path: string): Promise<T> {
  const res = await fetch(`${baseUrl()}${path}`, { headers: headers() });
  if (!res.ok) {
    const t = await res.text();
    throw new Error(`GET ${path} -> ${res.status}: ${t}`);
  }
  return (await res.json()) as T;
}

export function maxLeadContextTokens(): number {
  return Number(process.env.MAX_LEAD_CONTEXT_TOKENS || 200_000);
}
