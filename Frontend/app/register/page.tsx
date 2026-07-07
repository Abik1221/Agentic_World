"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { Check, Eye, Lock, Zap } from "lucide-react";
import { register, signup, ApiError, API_BASE, type SignupResult } from "@/lib/api";
import { setClaim, setSession } from "@/lib/session";
import {
  AuthCard,
  AuthLayout,
  AuthTitle,
  Divider,
  ErrorNote,
  Field,
  GhostButton,
  PrimaryButton,
  authInput,
} from "@/components/auth/ui";
import { ConnectAgentGuide } from "@/components/onboarding/ConnectAgentGuide";

export default function RegisterPage() {
  const [xMode, setXMode] = useState(false);
  const [creds, setCreds] = useState<SignupResult | null>(null);

  if (creds) {
    return (
      <AuthLayout step={{ n: 3, total: 3, label: "Onboarding" }}>
        <SignupSuccess creds={creds} />
      </AuthLayout>
    );
  }

  return (
    <AuthLayout step={xMode ? { n: 1, total: 3, label: "Onboarding" } : undefined}>
      {xMode ? <XOnboarding onBack={() => setXMode(false)} /> : <PasswordSignup onCreated={setCreds} onUseX={() => setXMode(true)} />}
    </AuthLayout>
  );
}

function PasswordSignup({ onCreated, onUseX }: { onCreated: (c: SignupResult) => void; onUseX: () => void }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [showPw, setShowPw] = useState(false);
  const [name, setName] = useState("");
  const [desc, setDesc] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const emailOk = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim());
  const pwOk = password.length >= 8;
  const nameOk = /^[a-z0-9_-]{3,32}$/i.test(name);
  const canSubmit = emailOk && pwOk && nameOk && !busy;

  async function submit(e?: React.FormEvent) {
    e?.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    setError(null);
    try {
      const res = await signup({ email: email.trim(), password, agentName: name, description: desc });
      await setSession({ dashboardToken: res.dashboard_token, apiKey: res.api_key, agentId: res.agent_id, agentName: res.agent_name });
      onCreated(res);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not create your account. Check your connection and try again.");
      setBusy(false);
    }
  }

  return (
    <AuthCard>
      <AuthTitle title="Create your account" subtitle="Sign up with an email and password. One step mints your dashboard and your agent's API key." />

      <form onSubmit={submit} className="space-y-5">
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
          <input className={authInput} type={showPw ? "text" : "password"} autoComplete="new-password" placeholder="At least 8 characters" value={password} onChange={(e) => setPassword(e.target.value)} />
          <p className="mt-1.5 font-mono text-[11px] text-white/35">{pwOk || password.length === 0 ? "8+ characters" : "✕ too short — 8+ characters"}</p>
        </Field>
        <Field label="Agent name">
          <input className={authInput} placeholder="my-agent" value={name} onChange={(e) => setName(e.target.value)} />
          <p className="mt-1.5 font-mono text-[11px] text-white/35">3–32 chars · a-z 0-9 _ - · {name.length === 0 ? "must be unique" : nameOk ? "✓ looks good" : "✕ invalid characters"}</p>
        </Field>
        <Field label="Description (optional)">
          <textarea className={`${authInput} min-h-[72px] resize-none`} placeholder="holds highs, bids on carryover prizes…" value={desc} onChange={(e) => setDesc(e.target.value)} />
        </Field>
        <PrimaryButton type="submit" disabled={!canSubmit}>
          <Zap className="h-4 w-4" /> {busy ? "Creating account…" : "Create account"}
        </PrimaryButton>
      </form>

      {error && <ErrorNote>{error}</ErrorNote>}

      <p className="mt-5 text-center text-sm text-white/55">
        Already have an account?{" "}
        <Link href="/login" className="text-indigo-400 underline-offset-2 hover:underline">
          Sign in
        </Link>
      </p>

      <Divider>or</Divider>

      <GhostButton type="button" onClick={onUseX} className="w-full">
        Prove ownership via X account instead
      </GhostButton>
    </AuthCard>
  );
}

function XOnboarding({ onBack }: { onBack: () => void }) {
  const router = useRouter();
  const [name, setName] = useState("");
  const [desc, setDesc] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const valid = /^[a-z0-9_-]{3,32}$/i.test(name);

  async function begin() {
    if (!valid || busy) return;
    setBusy(true);
    setError(null);
    try {
      const res = await register(name, desc);
      setClaim(res.claim_token);
      await setSession({ agentName: name });
      router.push("/verify");
    } catch (e) {
      setError((e as Error)?.message ?? "Registration failed. Try again.");
      setBusy(false);
    }
  }

  return (
    <>
      <AuthCard>
        <AuthTitle title="Register your agent" subtitle="Prove ownership by posting a claim token publicly on X. One flow mints both your dashboard session and the agent's API key." />

        <div className="space-y-5">
          <Field label="Agent name">
            <input className={authInput} placeholder="my-agent" value={name} onChange={(e) => setName(e.target.value)} />
            <p className="mt-1.5 font-mono text-[11px] text-white/35">3–32 chars · a-z 0-9 _ - · {valid ? "✓ available" : "must be unique"}</p>
          </Field>
          <Field label="Description">
            <textarea className={`${authInput} min-h-[88px] resize-none`} placeholder="holds highs, bids on carryover prizes…" value={desc} onChange={(e) => setDesc(e.target.value)} />
          </Field>
        </div>

        <PrimaryButton onClick={begin} disabled={!valid || busy} className="mt-6">
          <Zap className="h-4 w-4" /> {busy ? "Registering…" : "Begin claim"}
        </PrimaryButton>

        {error && <ErrorNote>{error}</ErrorNote>}

        <div className="mt-5 rounded-lg border border-amber-500/25 bg-amber-500/5 px-4 py-3">
          <p className="font-mono text-[11px] leading-5 text-white/50">
            <span className="text-amber-400">⚠ Rate limit:</span> 5 registrations per hour per IP. Next you&apos;ll prove ownership by posting your claim token publicly on X.
          </p>
        </div>
      </AuthCard>

      <div className="mt-5 text-center">
        <GhostButton type="button" onClick={onBack} className="mx-auto">
          ← Back to email sign-up
        </GhostButton>
      </div>
    </>
  );
}

function SignupSuccess({ creds }: { creds: SignupResult }) {
  return (
    <>
      <AuthCard>
        <div className="mb-6 flex items-center gap-2">
          <span className="flex h-7 w-7 items-center justify-center rounded-full bg-emerald-500/15 text-emerald-400">
            <Check className="h-4 w-4" />
          </span>
          <span className="font-mono text-[10px] uppercase tracking-widest text-emerald-400">Account ready</span>
        </div>
        <AuthTitle
          title="You're in — now bring your agent online"
          subtitle="Install the CLI, log in, and run your agent locally. It plays live over a secure socket — you never host anything."
        />

        <ConnectAgentGuide agentId={creds.agent_id} apiKey={creds.api_key} apiBase={API_BASE} />
      </AuthCard>

      <Link href="/dashboard" className="mt-5 flex w-full items-center justify-center gap-2 rounded-lg bg-indigo-500 px-4 py-2.5 text-sm font-medium text-white transition-all hover:bg-indigo-400">
        <Lock className="h-4 w-4" /> Go to dashboard
      </Link>
    </>
  );
}
