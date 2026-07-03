import { getSession } from "./session";

/** Show admin nav when backend has admins configured and user is signed in. */
export function showAdminNav(): boolean {
  const raw = process.env.NEXT_PUBLIC_ADMIN_USER_IDS ?? "";
  if (!raw.trim()) return false;
  return Boolean(getSession().dashboardToken);
}
