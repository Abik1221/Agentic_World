"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { setSession } from "@/lib/session";
import { verifyMagicLink } from "@/lib/api";
import { StatusScreen } from "@/components/auth/ui";

function VerifyInner() {
  const router = useRouter();
  const params = useSearchParams();
  const token = params.get("token") ?? "";
  const [state, setState] = useState<"verifying" | "ok" | "error">("verifying");
  const [message, setMessage] = useState("Restoring your session and agent access.");

  useEffect(() => {
    if (!token) {
      setState("error");
      setMessage("This link is missing its token. Request a new sign-in link.");
      return;
    }
    let active = true;
    verifyMagicLink(token)
      .then((r) => {
        if (!active) return;
        setSession({ dashboardToken: r.dashboard_token, apiKey: r.api_key, agentId: r.agent_id });
        setState("ok");
        setMessage("Session restored — taking you to your console.");
        setTimeout(() => router.push("/dashboard"), 900);
      })
      .catch((e) => {
        if (!active) return;
        setState("error");
        setMessage((e as Error)?.message ?? "This link is invalid or has expired.");
      });
    return () => {
      active = false;
    };
  }, [token, router]);

  return (
    <StatusScreen
      tone={state === "error" ? "error" : state === "ok" ? "ok" : "pending"}
      eyebrow="Magic link"
      title={state === "verifying" ? "Verifying your link…" : state === "ok" ? "You're in" : "Link problem"}
      message={message}
      cta={state === "error" ? { label: "Request a new link", href: "/login" } : undefined}
    />
  );
}

export default function VerifyPage() {
  return (
    <Suspense fallback={null}>
      <VerifyInner />
    </Suspense>
  );
}
