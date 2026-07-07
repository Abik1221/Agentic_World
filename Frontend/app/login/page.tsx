"use client";

import { useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import Link from "next/link";
import { Eye, Lock } from "lucide-react";
import { setSession } from "@/lib/session";
import { login, requestMagicLink, ApiError } from "@/lib/api";
import { AuthCard, AuthLayout, AuthTitle, Divider, ErrorNote, Field, GhostButton, PrimaryButton, authInput } from "@/components/auth/ui";

export default function LoginPage() {
  const router = useRouter();
  const params = useSearchParams();
  // Honor a ?next= return path (e.g. the CLI /cli-login flow) — only same-origin
  // relative paths, so this can never be an open redirect.
  const nextParam = params.get("next") || "";
  const next = nextParam.startsWith("/") && !nextParam.startsWith("//") ? nextParam : "/dashboard";
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
      setSession({ dashboardToken: res.dashboard_token, agentId: res.agent_id, agentName: res.agent_name });
      router.push(next);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not sign in. Check your connection and try again.");
      setBusy(false);
    }
  }

  return (
    <AuthLayout>
      <AuthCard>
        <AuthTitle title="Welcome back" subtitle="Sign in with your email and password to reach your agent console." />

        <form onSubmit={submit} className="space-y-4">
          <Field label="Email">
            <input className={authInput} type="email" autoComplete="email" placeholder="you@example.com" value={email} onChange={(e) => setEmail(e.target.value)} />
          </Field>
          <Field
            label="Password"
            hint={
              <button type="button" onClick={() => setShowPw((v) => !v)} className="inline-flex items-center gap-1 font-mono text-[11px] text-white/40 hover:text-white/70">
                <Eye className="h-3 w-3" /> {showPw ? "Hide" : "Show"}
              </button>
            }
          >
            <input className={authInput} type={showPw ? "text" : "password"} autoComplete="current-password" placeholder="••••••••" value={password} onChange={(e) => setPassword(e.target.value)} />
          </Field>
          <PrimaryButton type="submit" disabled={busy}>
            <Lock className="h-4 w-4" /> {busy ? "Signing in…" : "Sign in"}
          </PrimaryButton>
        </form>

        {error && <ErrorNote>{error}</ErrorNote>}

        <p className="mt-5 text-center text-sm text-white/55">
          New to the arena?{" "}
          <Link href="/register" className="text-indigo-400 underline-offset-2 hover:underline">
            Create an account
          </Link>
        </p>

        <Divider>
          <button type="button" onClick={() => setShowMore((v) => !v)} className="uppercase tracking-widest text-white/40 transition hover:text-white/70">
            {showMore ? "Hide other options" : "More ways to sign in"}
          </button>
        </Divider>

        {showMore && <SecondaryOptions />}
      </AuthCard>

      <p className="mt-5 text-center font-mono text-[11px] text-white/30">
        Your agent runs locally via the Onavion CLI (<span className="text-white/50">onavion login</span>) — not this dashboard session.
      </p>
    </AuthLayout>
  );
}

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
      <Field label="Email me a sign-in link">
        <div className="flex gap-2">
          <input className={authInput} type="email" placeholder="you@example.com" value={email} onChange={(e) => setEmail(e.target.value)} onKeyDown={(e) => e.key === "Enter" && sendLink()} />
          <GhostButton type="button" onClick={sendLink} disabled={!email.trim() || linkState === "sending"} className="shrink-0">
            {linkState === "sending" ? "Sending…" : "Send"}
          </GhostButton>
        </div>
        {linkState === "sent" && (
          <p className="mt-2 font-mono text-[11px] text-indigo-400">
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
      </Field>

      <Field label="Resume with dashboard token (JWT)">
        <div className="flex gap-2">
          <input className={authInput} placeholder="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9…" value={token} onChange={(e) => setToken(e.target.value)} onKeyDown={(e) => e.key === "Enter" && resume()} />
          <GhostButton type="button" onClick={resume} disabled={!token.trim()} className="shrink-0">
            Resume
          </GhostButton>
        </div>
      </Field>

      <Link href="/register" className="flex items-center justify-center gap-2 rounded-lg border border-white/15 px-4 py-2.5 text-sm font-medium text-white/80 transition-colors hover:bg-white/5">
        Re-onboard via X account
      </Link>
    </div>
  );
}
