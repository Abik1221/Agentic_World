"use client";

import * as React from "react";
import { useRouter } from "next/navigation";
import { usePrivy } from "@privy-io/react-auth";
import { Sparkles } from "lucide-react";
import { privyLogin, ApiError, type PrivyProfileHints } from "@/lib/api";
import { setSession } from "@/lib/session";
import { PrimaryButton } from "./ui";

type PrivyUser = ReturnType<typeof usePrivy>["user"];

// profileHints pulls best-effort display data off the Privy user. These are
// NON-AUTHORITATIVE — the backend trusts only the verified Privy id and stores
// these as hints (email, connected wallet, avatar). walletClientType is the
// provider ("phantom" / "solflare" / "backpack" / "privy" for the embedded one).
function profileHints(user: PrivyUser): PrivyProfileHints {
  if (!user) return {};
  const w = user.wallet;
  return {
    email: user.email?.address ?? user.google?.email ?? undefined,
    wallet_address: w?.address,
    wallet_provider: w?.walletClientType,
    display_name:
      user.google?.name ??
      user.twitter?.name ??
      user.discord?.username ??
      user.github?.name ??
      user.github?.username ??
      undefined,
    avatar_url: user.twitter?.profilePictureUrl ?? undefined,
  };
}

// PrivyLoginButton opens Privy's themed popup and, once Privy authenticates,
// exchanges its access token for our dashboard session exactly once, then
// navigates. Rendered only when Privy is configured (see PRIVY_ENABLED).
export function PrivyLoginButton({
  next,
  onError,
}: {
  next: string;
  onError?: (msg: string) => void;
}) {
  const router = useRouter();
  const { ready, authenticated, user, login, getAccessToken, logout } = usePrivy();
  const [exchanging, setExchanging] = React.useState(false);
  const exchangedRef = React.useRef(false);

  React.useEffect(() => {
    if (!authenticated || exchangedRef.current) return;
    exchangedRef.current = true;
    (async () => {
      setExchanging(true);
      try {
        const token = await getAccessToken();
        if (!token) throw new Error("no_token");
        const res = await privyLogin(token, profileHints(user));
        await setSession({ dashboardToken: res.dashboard_token });
        router.push(next);
      } catch (err) {
        // Our exchange failed but the Privy session is live — drop it so the user
        // isn't stuck "authenticated" with no app session, and can retry cleanly.
        exchangedRef.current = false;
        setExchanging(false);
        await logout().catch(() => {});
        onError?.(err instanceof ApiError ? err.message : "Could not complete sign-in. Please try again.");
      }
    })();
  }, [authenticated, user, getAccessToken, router, next, onError, logout]);

  const label = !ready ? "Loading…" : exchanging || authenticated ? "Signing you in…" : "Continue with Google, wallet & more";
  const busy = !ready || exchanging || authenticated;

  return (
    <PrimaryButton type="button" onClick={() => login()} disabled={busy}>
      <Sparkles className="h-4 w-4" /> {label}
    </PrimaryButton>
  );
}
