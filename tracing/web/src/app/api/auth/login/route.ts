import { cookies } from "next/headers";

import { SESSION_COOKIE, SESSION_TTL_SECONDS, authEnabled, checkCredentials, signSession } from "@/lib/auth";

export async function POST(request: Request): Promise<Response> {
  if (!authEnabled()) {
    return Response.json({ ok: false, error: "auth_disabled" }, { status: 400 });
  }
  let body: { user?: unknown; password?: unknown } = {};
  try {
    body = await request.json();
  } catch {
    /* empty body → invalid below */
  }
  const user = typeof body.user === "string" && body.user ? body.user : "admin";
  const password = typeof body.password === "string" ? body.password : "";

  if (!checkCredentials(user, password)) {
    return Response.json({ ok: false, error: "invalid_credentials" }, { status: 401 });
  }

  const store = await cookies();
  store.set(SESSION_COOKIE, signSession(user), {
    httpOnly: true,
    sameSite: "lax",
    secure: process.env.NODE_ENV === "production",
    path: "/",
    maxAge: SESSION_TTL_SECONDS,
  });
  return Response.json({ ok: true });
}
