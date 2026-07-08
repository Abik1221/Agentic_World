"use client";

import * as React from "react";
import { PrivyProvider } from "@privy-io/react-auth";

// Pyyol/Onavion login front door. Privy owns authentication — social (Google, X,
// GitHub, Discord), email, and Solana wallets, including embedded wallets for
// non-crypto users. Our Go backend verifies the Privy access token and exchanges
// it for a dashboard session (POST /v1/auth/privy). The popup is themed to match
// the matte-black + indigo auth kit (see components/auth/ui.tsx).
//
// Guarded: when NEXT_PUBLIC_PRIVY_APP_ID is unset the provider is skipped and the
// app renders normally — the existing email/password + magic-link login still
// works. This mirrors the backend, where an unconfigured Privy makes
// /v1/auth/privy return 503 rather than breaking the other auth paths.

const APP_ID = process.env.NEXT_PUBLIC_PRIVY_APP_ID;

/** True when a Privy app id is configured; drives whether the Privy CTA renders. */
export const PRIVY_ENABLED = Boolean(APP_ID);

export function PrivyAuthProvider({ children }: { children: React.ReactNode }) {
  if (!APP_ID) return <>{children}</>;
  return (
    <PrivyProvider
      appId={APP_ID}
      config={{
        appearance: {
          theme: "dark",
          accentColor: "#6366f1", // indigo-500 — same accent as the auth kit
          landingHeader: "Enter the arena",
          loginMessage: "Sign in to deploy agents, join matches, and manage your wallet.",
          // Beta is Solana-only: show Solana wallets (Phantom/Solflare/Backpack).
          walletChainType: "solana-only",
          showWalletLoginFirst: false,
          walletList: ["phantom", "solflare", "backpack", "detected_solana_wallets"],
        },
        loginMethods: ["google", "email", "github", "twitter", "discord", "wallet"],
        embeddedWallets: {
          // Non-crypto users get a Solana embedded wallet automatically at login
          // (only if they didn't connect an external one) → <60s onboarding.
          solana: { createOnLogin: "users-without-wallets" },
        },
      }}
    >
      {children}
    </PrivyProvider>
  );
}
