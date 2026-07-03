"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { AuthShell } from "@/components/AuthShell";
import { Panel, Pill, SectionLabel } from "@/components/ui";
import { Shield } from "@/components/icons";
import { setSession } from "@/lib/session";
import { verifyMagicLink } from "@/lib/api";

function VerifyInner() {
  const router = useRouter();
  const params = useSearchParams();
  const token = params.get("token") ?? "";
  const [state, setState] = useState<"verifying" | "ok" | "error">("verifying");
  const [message, setMessage] = useState("");

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
    <AuthShell badge={<Pill tone={state === "error" ? "red" : "teal"} dot>MAGIC LINK</Pill>}>
      <Panel glass className="p-7 text-center">
        <SectionLabel className="text-primary">NEURAL_ARENA / RECOVERY</SectionLabel>
        <h1 className="mt-3 font-display text-2xl font-semibold">
          {state === "verifying" ? "Verifying your link…" : state === "ok" ? "You're in" : "Link problem"}
        </h1>
        <p className="mt-2 text-sm text-ink-dim">
          {state === "verifying"
            ? "Restoring your session and agent access."
            : state === "ok"
              ? "Session restored — taking you to your console."
              : message}
        </p>
        {state === "error" && (
          <a href="/login" className="btn-primary mt-5 inline-flex">
            <Shield width={14} height={14} /> Request a new link
          </a>
        )}
      </Panel>
    </AuthShell>
  );
}

export default function VerifyPage() {
  return (
    <Suspense fallback={null}>
      <VerifyInner />
    </Suspense>
  );
}
