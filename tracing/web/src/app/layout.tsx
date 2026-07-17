import type { Metadata } from "next";
import "./globals.css";
import Shell from "./Shell";
import LoginScreen from "./LoginScreen";
import { fetchServiceHealth } from "@/lib/pyyol-lens-api";
import { authEnabled, currentUser } from "@/lib/auth";

export const metadata: Metadata = {
  title: "Pyyol Lens",
  description: "Traces, agent benchmarks, and cost observability for the Pyyol arena.",
};

export default async function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  // Central auth gate: when a password is configured and there's no valid
  // session, render the login screen instead of the app (and the API client
  // refuses to fetch data for unauthenticated requests — defense in depth).
  const gated = authEnabled();
  const user = gated ? await currentUser() : null;
  const locked = gated && !user;

  const health = locked ? undefined : await fetchServiceHealth();

  return (
    <html lang="en">
      <body>
        {locked ? (
          <LoginScreen />
        ) : (
          <Shell health={health} user={user ?? undefined}>
            {children}
          </Shell>
        )}
      </body>
    </html>
  );
}
