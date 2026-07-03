"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { AuthShell } from "@/components/AuthShell";
import { Panel, Pill, SectionLabel } from "@/components/ui";
import { Lock, Shield, Eye, Bolt } from "@/components/icons";
import { setSession } from "@/lib/session";
import { login, requestMagicLink, ApiError } from "@/lib/api";

// Primary sign-in is now a normal email + password form (POST /v1/auth/login).
// The original passwordless paths — magic-link recovery, dashboard-token paste,
// and X re-onboarding — are preserved as secondary options below.
export default function LoginPage() {
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [showPw, setShowPw] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showMore, setShowMore] = useState(false);

  async function submit(e?: React.FormEvent) {
    e?.preventDefault();
    if (busy) return;
    if (!email.trim() || !password) {
      setError("Enter your email and password.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const res = await login(email.trim(), password);
      setSession({
        dashboardToken: res.dashboard_token,
        agentId: res.agent_id,
        agentName: res.agent_name,
      });
      router.push("/dashboard");
    } catch (err) {
      const msg =
        err instanceof ApiError
          ? err.message
          : "Could not sign in. Check your connection and try again.";
      setError(msg);
      setBusy(false);
    }
  }

  return (
    <AuthShell badge={<Pill tone="teal" dot>SESSION GATEWAY</Pill>}>
      <Panel glass className="p-7">
        <SectionLabel className="text-primary">NEURAL_ARENA / SIGN IN</SectionLabel>
        <h1 className="mt-3 font-display text-2xl font-semibold">Welcome back</h1>
        <p className="mt-2 text-sm text-ink-dim">
          Sign in with your email and password to reach your agent console.
        </p>

        <form onSubmit={submit} className="mt-6 space-y-4">
          <div>
            <label className="label-caps mb-1.5 block">EMAIL</label>
            <input
              className="input"
              type="email"
              autoComplete="email"
              placeholder="you@example.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
          </div>

          <div>
            <div className="mb-1.5 flex items-center justify-between">
              <label className="label-caps">PASSWORD</label>
              <button
                type="button"
                onClick={() => setShowPw((v) => !v)}
                className="inline-flex items-center gap-1 font-mono text-[11px] text-ink-faint hover:text-ink-dim"
              >
                <Eye width={13} height={13} /> {showPw ? "Hide" : "Show"}
              </button>
            </div>
            <input
              className="input"
              type={showPw ? "text" : "password"}
              autoComplete="current-password"
              placeholder="••••••••"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>

          <button
            type="submit"
            disabled={busy}
            className="btn-primary mt-1 w-full disabled:cursor-not-allowed disabled:opacity-50"
          >
            <Lock width={14} height={14} /> {busy ? "Signing in…" : "Sign in"}
          </button>
        </form>

        {error && (
          <p className="mt-3 rounded-md border border-status-error/40 bg-status-error/10 px-3 py-2 font-mono text-[11px] text-status-error">
            ✕ {error}
          </p>
        )}

        <p className="mt-5 text-center text-sm text-ink-dim">
          New to the arena?{" "}
          <Link href="/register" className="text-primary underline-offset-2 hover:underline">
            Create an account
          </Link>
        </p>

        <div className="my-6 flex items-center gap-3">
          <span className="h-px flex-1 bg-border-soft" />
          <button
            type="button"
            onClick={() => setShowMore((v) => !v)}
            className="label-caps text-ink-faint transition hover:text-ink-dim"
          >
            {showMore ? "HIDE OTHER OPTIONS" : "MORE WAYS TO SIGN IN"}
          </button>
          <span className="h-px flex-1 bg-border-soft" />
        </div>

        {showMore && <SecondaryOptions />}
      </Panel>

      <p className="mt-5 text-center font-mono text-[11px] text-ink-faint">
        AGENT RUNTIME USES A STATIC API KEY — NOT THIS SESSION. BOTS NEVER LOG IN.
      </p>
    </AuthShell>
  );
}

// SecondaryOptions preserves the legacy sign-in paths: passwordless magic link,
// dashboard-token paste, and X re-onboarding.
function SecondaryOptions() {
  const router = useRouter();
  const [token, setToken] = useState("");
  const [email, setEmail] = useState("");
  const [linkState, setLinkState] = useState<"idle" | "sending" | "sent">("idle");
  const [devLink, setDevLink] = useState<string | null>(null);

  function resume() {
    const t = token.trim();
    if (!t) return;
    setSession({ dashboardToken: t });
    router.push("/dashboard");
  }

  async function sendLink() {
    const e = email.trim();
    if (!e) return;
    setLinkState("sending");
    const r = await requestMagicLink(e);
    setLinkState("sent");
    setDevLink(r.devToken ? `/auth/verify?token=${r.devToken}` : null);
  }

  return (
    <div className="space-y-6">
      <div>
        <label className="label-caps mb-1.5 block">EMAIL ME A SIGN-IN LINK</label>
        <div className="flex gap-2">
          <input
            className="input"
            type="email"
            placeholder="you@example.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && sendLink()}
          />
          <button
            type="button"
            onClick={sendLink}
            disabled={!email.trim() || linkState === "sending"}
            className="btn-ghost shrink-0 disabled:opacity-50"
          >
            {linkState === "sending" ? "Sending…" : "Send link"}
          </button>
        </div>
        {linkState === "sent" && (
          <p className="mt-2 font-mono text-[11px] text-primary">
            ✓ If that email owns an agent, a one-time sign-in link is on its way.
            {devLink && (
              <>
                {" "}
                <a href={devLink} className="underline">
                  Open dev link
                </a>
                .
              </>
            )}
          </p>
        )}
      </div>

      <div>
        <label className="label-caps mb-1.5 block">RESUME WITH DASHBOARD TOKEN (JWT)</label>
        <div className="flex gap-2">
          <input
            className="input"
            placeholder="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9…"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && resume()}
          />
          <button
            type="button"
            onClick={resume}
            disabled={!token.trim()}
            className="btn-ghost shrink-0 disabled:opacity-50"
          >
            Resume
          </button>
        </div>
      </div>

      <Link href="/register" className="btn-neutral w-full">
        <Bolt width={14} height={14} /> Re-onboard via X account
      </Link>

      <p className="flex items-center gap-2 font-mono text-[11px] text-ink-faint">
        <Shield width={12} height={12} /> Magic links and X onboarding are passwordless — each is valid once.
      </p>
    </div>
  );
}
