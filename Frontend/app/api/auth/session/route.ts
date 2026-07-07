import { NextRequest, NextResponse } from "next/server";

// Session cookie writer (BFF). Secrets — the dashboard JWT (aa_dash) and agent API
// key (aa_key) — are stored HttpOnly so JS (and thus XSS) can never read them; the
// same-origin /api/be proxy injects them into upstream calls. Non-secret markers
// (aa_has_dash/aa_has_key) + display fields (aa_agent/aa_name) are readable so the
// client can tell it's signed in and show the agent id/name.

const DAY = 60 * 60 * 24; // matches the dashboard-token TTL

export async function POST(req: NextRequest) {
  const b = (await req.json().catch(() => ({}))) as {
    dashboard_token?: string;
    api_key?: string;
    agent_id?: string;
    agent_name?: string;
  };
  const res = NextResponse.json({ ok: true });
  const secure = process.env.NODE_ENV === "production";
  const secret = { httpOnly: true, secure, sameSite: "lax" as const, path: "/", maxAge: DAY };
  const marker = { httpOnly: false, secure, sameSite: "lax" as const, path: "/", maxAge: DAY };

  if (b.dashboard_token) {
    res.cookies.set("aa_dash", b.dashboard_token, secret);
    res.cookies.set("aa_has_dash", "1", marker);
  }
  if (b.api_key) {
    res.cookies.set("aa_key", b.api_key, secret);
    res.cookies.set("aa_has_key", "1", marker);
  }
  if (b.agent_id) res.cookies.set("aa_agent", b.agent_id, marker);
  if (b.agent_name) res.cookies.set("aa_name", b.agent_name, marker);
  return res;
}

export async function DELETE() {
  const res = NextResponse.json({ ok: true });
  for (const n of ["aa_dash", "aa_key", "aa_agent", "aa_name", "aa_has_dash", "aa_has_key", "aa_claim"]) {
    res.cookies.set(n, "", { path: "/", maxAge: 0 });
  }
  return res;
}
