// ---------------------------------------------------------------------------
// Server-side session reader. Used by server components to pull the user's
// dashboard token / agent API key out of the request cookies so they can call
// authenticated backend endpoints during render. Keep this file out of client
// components — it imports `next/headers`.
// ---------------------------------------------------------------------------
import { cookies } from "next/headers";
import { COOKIE, type Session } from "./session";

export function serverSession(): Session {
  const jar = cookies();
  return {
    dashboardToken: jar.get(COOKIE.dash)?.value,
    apiKey: jar.get(COOKIE.key)?.value,
    agentId: jar.get(COOKIE.agent)?.value,
    agentName: jar.get(COOKIE.name)?.value,
  };
}
