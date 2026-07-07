import { cookies } from "next/headers";
import { NextRequest } from "next/server";

// BFF proxy: the browser calls same-origin /api/be/v1/… with an `x-pyyol-scope`
// hint (user|agent); this server route reads the matching HttpOnly cookie and
// injects it as the upstream Bearer, so the token never touches client JS. Public
// calls (no scope) forward unauthenticated. Server components bypass this entirely
// — they read the real cookie via serverSession and call the API directly.

const API = (process.env.API_BASE_INTERNAL || process.env.NEXT_PUBLIC_API_BASE || "http://localhost:8080").replace(/\/$/, "");

async function proxy(req: NextRequest, ctx: { params: Promise<{ path: string[] }> }) {
  const { path } = await ctx.params;
  const search = new URL(req.url).search;
  const target = `${API}/${(path ?? []).join("/")}${search}`;

  const scope = req.headers.get("x-pyyol-scope") ?? "";
  const jar = await cookies();
  const token = scope === "agent" ? jar.get("aa_key")?.value : scope === "user" ? jar.get("aa_dash")?.value : undefined;

  const headers = new Headers({ accept: "application/json" });
  const ct = req.headers.get("content-type");
  if (ct) headers.set("content-type", ct);
  if (token) headers.set("authorization", `Bearer ${token}`);

  const method = req.method.toUpperCase();
  const body = method === "GET" || method === "HEAD" ? undefined : await req.text();

  let upstream: Response;
  try {
    upstream = await fetch(target, { method, headers, body: body || undefined, cache: "no-store" });
  } catch {
    return new Response(JSON.stringify({ error: "upstream_unreachable" }), {
      status: 502,
      headers: { "content-type": "application/json" },
    });
  }
  const text = await upstream.text();
  return new Response(text, {
    status: upstream.status,
    headers: { "content-type": upstream.headers.get("content-type") ?? "application/json" },
  });
}

export const GET = proxy;
export const POST = proxy;
export const PUT = proxy;
export const PATCH = proxy;
export const DELETE = proxy;
