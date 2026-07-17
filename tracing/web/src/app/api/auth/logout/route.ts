import { cookies } from "next/headers";

import { SESSION_COOKIE } from "@/lib/auth";

export async function POST(): Promise<Response> {
  const store = await cookies();
  store.set(SESSION_COOKIE, "", { httpOnly: true, path: "/", maxAge: 0 });
  return Response.json({ ok: true });
}
