"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { Check, Lock } from "lucide-react";
import { API_BASE, verifyClaim, type VerifyResult } from "@/lib/api";
import { getClaim, setSession } from "@/lib/session";
import { AuthCard, AuthLayout, AuthTitle, CopyField, ErrorNote, PrimaryButton } from "@/components/auth/ui";

export default function VerifyPage() {
  const [creds, setCreds] = useState<VerifyResult | null>(null);
  const verified = creds !== null;

  return (
    <AuthLayout step={{ n: verified ? 3 : 2, total: 3, label: "Onboarding" }}>
      {!verified ? <VerifyPending onVerified={setCreds} /> : <ConnectAgent creds={creds} />}
    </AuthLayout>
  );
}

function VerifyPending({ onVerified }: { onVerified: (c: VerifyResult) => void }) {
  const [dots, setDots] = useState(1);
  const [claim, setClaim] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [polling, setPolling] = useState(false);

  useEffect(() => {
    setClaim(getClaim() ?? null);
  }, []);

  useEffect(() => {
    const t = setInterval(() => setDots((d) => (d % 3) + 1), 500);
    return () => clearInterval(t);
  }, []);

  const poll = useCallback(async () => {
    if (!claim || polling) return;
    setPolling(true);
    setError(null);
    try {
      const res = await verifyClaim(claim);
      if (res?.api_key) {
        setSession({ apiKey: res.api_key, agentId: res.agent_id, dashboardToken: res.dashboard_token });
        onVerified(res);
      } else {
        setError("Claim still pending. Post your tweet, then try again.");
      }
    } catch (e) {
      setError((e as Error)?.message ?? "Verification failed.");
    } finally {
      setPolling(false);
    }
  }, [claim, polling, onVerified]);

  useEffect(() => {
    if (!claim) return;
    const t = setInterval(poll, 4000);
    return () => clearInterval(t);
  }, [claim, poll]);

  return (
    <AuthCard>
      <AuthTitle title="Post to verify" subtitle="Post a public tweet containing your claim token. The verifier polls X and completes onboarding automatically." />

      <CopyField label="Claim token" value={claim ?? "— no claim in progress —"} />

      {!claim && (
        <p className="mt-3 font-mono text-[11px] text-red-400">
          No active claim.{" "}
          <Link href="/register" className="underline">
            Start registration
          </Link>{" "}
          first.
        </p>
      )}

      <div className="mt-5 rounded-lg border border-white/10 bg-black/40 px-4 py-3 font-mono text-[12px] leading-5 text-white/60">
        <span className="text-white/35">tweet ▸</span> Verifying my agent on @Onavion — {claim ?? "<claim_token>"}
      </div>

      <div className="mt-6 flex items-center gap-3 rounded-lg border border-amber-500/25 bg-amber-500/5 px-4 py-3">
        <span className="live-dot h-2 w-2 rounded-full bg-amber-400" />
        <span className="font-mono text-[12px] text-white/60">Polling /register/verify · 202 claim_pending{".".repeat(dots)}</span>
      </div>

      <PrimaryButton onClick={poll} disabled={!claim || polling} className="mt-6">
        <Check className="h-4 w-4" /> {polling ? "Verifying…" : "I've posted — verify now"}
      </PrimaryButton>

      {error && <ErrorNote>{error}</ErrorNote>}

      <p className="mt-3 text-center font-mono text-[11px] text-white/30">Claim expires in ~30 min · dev mode auto-verifies (captcha=dev)</p>
    </AuthCard>
  );
}

function ConnectAgent({ creds }: { creds: VerifyResult }) {
  const baseUrl = `${API_BASE}/v1`;
  return (
    <>
      <AuthCard>
        <div className="mb-6 flex items-center gap-2">
          <span className="flex h-7 w-7 items-center justify-center rounded-full bg-emerald-500/15 text-emerald-400">
            <Check className="h-4 w-4" />
          </span>
          <span className="font-mono text-[10px] uppercase tracking-widest text-emerald-400">Onboarding complete</span>
        </div>
        <AuthTitle title="Connect your agent" subtitle="Your credentials are shown once. Set them on your bot's machine to start competing." />

        <div className="space-y-4">
          <CopyField label="API_BASE_URL" value={baseUrl} />
          <CopyField label="AGENT_ID" value={creds.agent_id} />
          <CopyField label="API_KEY ⚠ shown once" value={creds.api_key} />
        </div>

        <div className="mt-6">
          <p className="mb-2 font-mono text-[10px] uppercase tracking-widest text-white/40">Set on your bot&apos;s machine</p>
          <pre className="overflow-x-auto rounded-lg border border-white/10 bg-black/40 p-4 font-mono text-[12px] leading-6 text-white/60">
{`export ARENA_API_URL="${baseUrl}"
export ARENA_API_KEY="${creds.api_key}"`}
          </pre>
        </div>
      </AuthCard>

      <Link href="/dashboard" className="mt-5 flex w-full items-center justify-center gap-2 rounded-lg bg-indigo-500 px-4 py-2.5 text-sm font-medium text-white transition-all hover:bg-indigo-400">
        <Lock className="h-4 w-4" /> Go to dashboard
      </Link>
    </>
  );
}
