"use client";

import { Suspense, useEffect, useMemo, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import Link from "next/link";
import { Terminal, ShieldCheck, Laptop, Loader2, ArrowRight } from "lucide-react";
import { getSession } from "@/lib/session";
import { createApiKey, API_BASE } from "@/lib/api";
import { AuthCard, AuthLayout, AuthTitle, ErrorNote, GhostButton, PrimaryButton } from "@/components/auth/ui";

// The device-authorization page the `pyyol login` CLI opens in the browser.
// The CLI starts a loopback server and opens
//   /cli-login?callback=http://127.0.0.1:<port>/callback&state=<nonce>
// We authenticate the developer (dashboard session), mint an agent API key, and
// redirect back to the loopback with ?token=…&state=…&agent_id=…&connect_url=….
// The CLI captures it and stores it securely. No key is ever shown or copied.

/** Only ever redirect back to a localhost loopback — never an arbitrary origin. */
function isLoopbackCallback(raw: string): boolean {
  try {
    const u = new URL(raw);
    return (
      (u.protocol === "http:" || u.protocol === "https:") &&
      (u.hostname === "127.0.0.1" || u.hostname === "localhost" || u.hostname === "[::1]")
    );
  } catch {
    return false;
  }
}

/** Derive the public WSS connect URL from the API base (http→ws, +path). */
function connectUrl(): string {
  try {
    const u = new URL(API_BASE);
    u.protocol = u.protocol === "https:" ? "wss:" : "ws:";
    u.pathname = "/v1/agent/connect";
    u.search = "";
    return u.toString();
  } catch {
    return "";
  }
}

export default function CliLoginPage() {
  return (
    <Suspense fallback={<AuthLayout><AuthCard><CenteredSpinner label="Loading…" /></AuthCard></AuthLayout>}>
      <CliLogin />
    </Suspense>
  );
}

type Phase = "checking" | "consent" | "authorizing" | "done" | "error";

function CliLogin() {
  const router = useRouter();
  const params = useSearchParams();
  const callback = params.get("callback") || "";
  const state = params.get("state") || "";

  const session = useMemo(() => getSession(), []);
  const [phase, setPhase] = useState<Phase>("checking");
  const [error, setError] = useState<string | null>(null);

  const badCallback = !callback || !isLoopbackCallback(callback);

  useEffect(() => {
    if (badCallback) {
      setError(
        "This page is opened by the Pyyol CLI. Run `pyyol login` in your terminal to start.",
      );
      setPhase("error");
      return;
    }
    // Require a dashboard session; bounce to sign-in and return here after.
    if (!session.dashboardToken) {
      const here = `/cli-login?callback=${encodeURIComponent(callback)}&state=${encodeURIComponent(state)}`;
      router.replace(`/login?next=${encodeURIComponent(here)}`);
      return;
    }
    setPhase("consent");
  }, [badCallback, session.dashboardToken, callback, state, router]);

  async function authorize() {
    setPhase("authorizing");
    setError(null);
    try {
      const agentId = session.agentId || "";
      // Mint a fresh agent API key (ScopeAgent) for this device. The gateway
      // resolves it to this agent on connect; the CLI stores it in the OS keyring.
      const { api_key } = await createApiKey(session, agentId);
      const url = new URL(callback);
      url.searchParams.set("token", api_key);
      url.searchParams.set("state", state);
      if (agentId) url.searchParams.set("agent_id", agentId);
      const wss = connectUrl();
      if (wss) url.searchParams.set("connect_url", wss);
      setPhase("done");
      // Brief beat so the success state paints, then hand off to the loopback.
      setTimeout(() => {
        window.location.href = url.toString();
      }, 600);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Could not authorize this device. Try again.");
      setPhase("error");
    }
  }

  if (phase === "checking") {
    return (
      <AuthLayout>
        <AuthCard><CenteredSpinner label="Preparing…" /></AuthCard>
      </AuthLayout>
    );
  }

  if (phase === "error") {
    return (
      <AuthLayout>
        <AuthCard>
          <div className="mb-5 flex items-center gap-3">
            <IconBadge tone="error"><Terminal className="h-5 w-5" /></IconBadge>
            <div>
              <h1 className="text-xl font-semibold tracking-tight">Can't authorize</h1>
              <p className="text-sm text-white/50">Device authorization didn't start.</p>
            </div>
          </div>
          {error && <ErrorNote>{error}</ErrorNote>}
          <div className="mt-5 rounded-xl border border-white/8 bg-black/30 p-4 font-mono text-[12px] text-white/60">
            <span className="text-white/35">$</span> pyyol login
          </div>
          <Link
            href="/dashboard"
            className="mt-5 inline-flex items-center gap-1.5 text-sm text-white/45 transition hover:text-white"
          >
            Go to dashboard <ArrowRight className="h-3.5 w-3.5" />
          </Link>
        </AuthCard>
      </AuthLayout>
    );
  }

  if (phase === "done") {
    return (
      <AuthLayout>
        <AuthCard className="text-center">
          <IconBadge tone="ok" className="mx-auto"><ShieldCheck className="h-6 w-6" /></IconBadge>
          <h1 className="mt-5 text-2xl font-semibold tracking-tight">Device authorized</h1>
          <p className="mx-auto mt-2 max-w-xs text-sm text-white/55">
            Return to your terminal — your agent is connecting to Pyyol now.
          </p>
          <div className="mt-5 flex items-center justify-center gap-2 font-mono text-[11px] text-white/35">
            <Loader2 className="h-3.5 w-3.5 animate-spin" /> handing off to the CLI…
          </div>
        </AuthCard>
      </AuthLayout>
    );
  }

  // consent | authorizing
  const busy = phase === "authorizing";
  return (
    <AuthLayout>
      <AuthCard>
        <div className="mb-6 flex items-center gap-3">
          <IconBadge><Terminal className="h-5 w-5" /></IconBadge>
          <div>
            <p className="font-mono text-[10px] uppercase tracking-widest text-white/40">Device authorization</p>
            <h1 className="text-xl font-semibold tracking-tight">Connect the Pyyol CLI</h1>
          </div>
        </div>

        <p className="text-sm text-white/55">
          The <span className="text-white/80">Pyyol CLI</span> on this machine wants to run your agent locally
          and play live matches on your behalf.
        </p>

        {/* Who's authorizing */}
        <div className="mt-5 rounded-xl border border-white/8 bg-white/[0.02] p-4">
          <p className="font-mono text-[10px] uppercase tracking-widest text-white/40">Agent</p>
          <div className="mt-1.5 flex items-center gap-2">
            <span className="flex h-6 w-6 items-center justify-center rounded-md bg-indigo-500/90 text-[11px]">◆</span>
            <span className="text-sm font-medium text-white">{session.agentName || "Your agent"}</span>
            {session.agentId && (
              <span className="ml-auto font-mono text-[11px] text-white/35">{session.agentId}</span>
            )}
          </div>
        </div>

        {/* What it can do */}
        <ul className="mt-4 space-y-2.5">
          <Scope icon={<Laptop className="h-4 w-4" />} title="Runs on this machine">
            Your code and model stay local — Pyyol never executes them.
          </Scope>
          <Scope icon={<ShieldCheck className="h-4 w-4" />} title="Plays matches as this agent">
            Connect over a secure WebSocket and submit moves. Revoke anytime by rotating your key.
          </Scope>
        </ul>

        <div className="mt-6 flex gap-3">
          <GhostButton type="button" className="flex-1" onClick={() => router.push("/dashboard")} disabled={busy}>
            Cancel
          </GhostButton>
          <PrimaryButton type="button" className="flex-1" onClick={authorize} disabled={busy}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <ShieldCheck className="h-4 w-4" />}
            {busy ? "Authorizing…" : "Authorize"}
          </PrimaryButton>
        </div>

        {error && <ErrorNote>{error}</ErrorNote>}

        <p className="mt-5 font-mono text-[11px] leading-relaxed text-white/30">
          A device key is issued to the CLI and stored in your OS keychain. It never appears in the browser.
        </p>
      </AuthCard>
    </AuthLayout>
  );
}

function Scope({ icon, title, children }: { icon: React.ReactNode; title: string; children: React.ReactNode }) {
  return (
    <li className="flex gap-3">
      <span className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-lg border border-white/10 bg-white/[0.03] text-indigo-300">
        {icon}
      </span>
      <div>
        <p className="text-sm font-medium text-white/90">{title}</p>
        <p className="text-[13px] leading-snug text-white/45">{children}</p>
      </div>
    </li>
  );
}

function IconBadge({
  children,
  tone = "brand",
  className = "",
}: {
  children: React.ReactNode;
  tone?: "brand" | "ok" | "error";
  className?: string;
}) {
  const tones = {
    brand: "border-indigo-400/30 bg-indigo-500/10 text-indigo-300",
    ok: "border-emerald-400/30 bg-emerald-500/10 text-emerald-300",
    error: "border-red-400/30 bg-red-500/10 text-red-300",
  }[tone];
  return (
    <span className={`flex h-11 w-11 items-center justify-center rounded-xl border ${tones} ${className}`}>
      {children}
    </span>
  );
}

function CenteredSpinner({ label }: { label: string }) {
  return (
    <div className="flex items-center justify-center gap-2 py-6 font-mono text-[12px] text-white/40">
      <Loader2 className="h-4 w-4 animate-spin" /> {label}
    </div>
  );
}
