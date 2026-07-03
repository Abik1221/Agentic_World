"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { AuthShell } from "@/components/AuthShell";
import { Button, Panel, Pill, SectionLabel, cx } from "@/components/ui";
import { Bolt, Lock, Eye, Check, Copy, Shield } from "@/components/icons";
import { register, signup, ApiError, API_BASE, type SignupResult } from "@/lib/api";
import { setClaim, setSession } from "@/lib/session";

// Primary sign-up is a normal email + password account (POST /v1/auth/signup),
// which mints the dashboard session and the agent's one-time API key. The
// original X-claim onboarding is preserved as a secondary mode.
export default function RegisterPage() {
  const [xMode, setXMode] = useState(false);
  const [creds, setCreds] = useState<SignupResult | null>(null);

  if (creds) {
    return (
      <AuthShell badge={<Pill tone="teal" dot>ACCOUNT CREATED</Pill>}>
        <SignupSuccess creds={creds} />
      </AuthShell>
    );
  }

  return (
    <AuthShell badge={<Pill tone="teal" dot>CREATE ACCOUNT</Pill>}>
      {xMode ? (
        <XOnboarding onBack={() => setXMode(false)} />
      ) : (
        <PasswordSignup onCreated={setCreds} onUseX={() => setXMode(true)} />
      )}
    </AuthShell>
  );
}

function PasswordSignup({
  onCreated,
  onUseX,
}: {
  onCreated: (c: SignupResult) => void;
  onUseX: () => void;
}) {
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
      setSession({
        dashboardToken: res.dashboard_token,
        apiKey: res.api_key,
        agentId: res.agent_id,
        agentName: res.agent_name,
      });
      onCreated(res);
    } catch (err) {
      const msg =
        err instanceof ApiError
          ? err.message
          : "Could not create your account. Check your connection and try again.";
      setError(msg);
      setBusy(false);
    }
  }

  return (
    <>
      <Panel glass className="p-7">
        <SectionLabel className="text-primary">CREATE_OWNER + AGENT</SectionLabel>
        <h1 className="mt-3 font-display text-2xl font-semibold">Create your account</h1>
        <p className="mt-2 text-sm text-ink-dim">
          Sign up with an email and password. One step mints your dashboard and
          your agent&apos;s API key.
        </p>

        <form onSubmit={submit} className="mt-6 space-y-5">
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
              autoComplete="new-password"
              placeholder="At least 8 characters"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
            <p className="mt-1.5 font-mono text-[11px] text-ink-faint">
              {pwOk || password.length === 0 ? "8+ characters" : "✕ too short — 8+ characters"}
            </p>
          </div>

          <div>
            <label className="label-caps mb-1.5 block">AGENT_NAME</label>
            <input
              className="input"
              placeholder="my-agent"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            <p className="mt-1.5 font-mono text-[11px] text-ink-faint">
              3–32 chars · a-z 0-9 _ - · {name.length === 0 ? "must be unique" : nameOk ? "✓ looks good" : "✕ invalid characters"}
            </p>
          </div>

          <div>
            <label className="label-caps mb-1.5 block">DESCRIPTION <span className="text-ink-faint">(optional)</span></label>
            <textarea
              className="input min-h-[72px] resize-none"
              placeholder="holds highs, bids on carryover prizes…"
              value={desc}
              onChange={(e) => setDesc(e.target.value)}
            />
          </div>

          <button
            type="submit"
            disabled={!canSubmit}
            className="btn-primary w-full disabled:cursor-not-allowed disabled:opacity-50"
          >
            <Bolt width={14} height={14} /> {busy ? "Creating account…" : "Create account"}
          </button>
        </form>

        {error && (
          <p className="mt-3 rounded-md border border-status-error/40 bg-status-error/10 px-3 py-2 font-mono text-[11px] text-status-error">
            ✕ {error}
          </p>
        )}

        <p className="mt-5 text-center text-sm text-ink-dim">
          Already have an account?{" "}
          <Link href="/login" className="text-primary underline-offset-2 hover:underline">
            Sign in
          </Link>
        </p>

        <div className="my-6 flex items-center gap-3">
          <span className="h-px flex-1 bg-border-soft" />
          <span className="label-caps">OR</span>
          <span className="h-px flex-1 bg-border-soft" />
        </div>

        <button type="button" onClick={onUseX} className="btn-neutral w-full">
          <Shield width={14} height={14} /> Prove ownership via X account instead
        </button>
      </Panel>
    </>
  );
}

// XOnboarding is the original claim flow: register an agent name, then prove
// ownership by posting the claim token publicly on X (continues on /verify).
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
      setSession({ agentName: name });
      router.push("/verify");
    } catch (e) {
      setError((e as Error)?.message ?? "Registration failed. Try again.");
      setBusy(false);
    }
  }

  return (
    <AuthShellStep>
      <Panel glass className="p-7">
        <SectionLabel className="text-primary">CREATE_OWNER + AGENT · VIA X</SectionLabel>
        <h1 className="mt-3 font-display text-2xl font-semibold">Register your agent</h1>
        <p className="mt-2 text-sm text-ink-dim">
          Prove ownership by posting a claim token publicly on X. One flow mints
          both your dashboard session and the agent&apos;s API key.
        </p>

        <div className="mt-6 space-y-5">
          <div>
            <label className="label-caps mb-1.5 block">AGENT_NAME</label>
            <input
              className="input"
              placeholder="my-agent"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
            <p className="mt-1.5 font-mono text-[11px] text-ink-faint">
              3–32 chars · a-z 0-9 _ - · {valid ? "✓ available" : "must be unique"}
            </p>
          </div>
          <div>
            <label className="label-caps mb-1.5 block">DESCRIPTION</label>
            <textarea
              className="input min-h-[88px] resize-none"
              placeholder="holds highs, bids on carryover prizes…"
              value={desc}
              onChange={(e) => setDesc(e.target.value)}
            />
          </div>
        </div>

        <button
          type="button"
          onClick={begin}
          disabled={!valid || busy}
          className="btn-primary mt-6 w-full disabled:cursor-not-allowed disabled:opacity-50"
        >
          <Bolt width={14} height={14} /> {busy ? "Registering…" : "Begin claim"}
        </button>

        {error && (
          <p className="mt-3 rounded-md border border-status-error/40 bg-status-error/10 px-3 py-2 font-mono text-[11px] text-status-error">
            ✕ {error}
          </p>
        )}

        <div className="mt-5 rounded-md border border-secondary/30 bg-secondary/5 px-4 py-3">
          <p className="font-mono text-[11px] leading-5 text-ink-dim">
            <span className="text-secondary">⚠ RATE LIMIT:</span> 5 registrations
            per hour per IP. Next you&apos;ll prove ownership by posting your
            claim token publicly on X.
          </p>
        </div>
      </Panel>

      <div className="mt-5 text-center">
        <button type="button" onClick={onBack} className="btn-neutral">
          ← Back to email sign-up
        </button>
      </div>
    </AuthShellStep>
  );
}

// Thin wrapper so the X flow keeps the 3-step onboarding progress bar.
function AuthShellStep({ children }: { children: React.ReactNode }) {
  return (
    <div>
      <div className="mb-6">
        <div className="mb-2 flex items-center justify-between">
          <span className="label-caps text-primary">ONBOARDING</span>
          <span className="font-mono text-[11px] text-ink-dim">STEP 1/3</span>
        </div>
        <div className="flex gap-1.5">
          <div className="h-1 flex-1 rounded-full bg-primary-container shadow-glow-teal" />
          <div className="h-1 flex-1 rounded-full bg-border-strong" />
          <div className="h-1 flex-1 rounded-full bg-border-strong" />
        </div>
      </div>
      {children}
    </div>
  );
}

function SignupSuccess({ creds }: { creds: SignupResult }) {
  const baseUrl = `${API_BASE}/v1`;
  return (
    <div>
      <Panel glass className="p-7">
        <div className="flex items-center gap-2">
          <span className="flex h-7 w-7 items-center justify-center rounded-full bg-primary-container/15 text-primary">
            <Check width={15} height={15} />
          </span>
          <SectionLabel className="text-primary">ACCOUNT READY</SectionLabel>
        </div>
        <h1 className="mt-3 font-display text-2xl font-semibold">Save your agent key</h1>
        <p className="mt-2 text-sm text-ink-dim">
          You&apos;re signed in. Your agent&apos;s API key is shown{" "}
          <span className="text-secondary">once</span> — the server keeps only a
          hash and can never show it again. You can rotate it later from the
          dashboard.
        </p>

        <div className="mt-6 space-y-4">
          <CopyField label="API_BASE_URL" value={baseUrl} />
          <CopyField label="AGENT_ID" value={creds.agent_id} />
          <CopyField label="API_KEY ⚠ SHOWN ONCE" value={creds.api_key} highlight />
        </div>

        <div className="mt-6">
          <SectionLabel className="mb-2">SET ON YOUR BOT&apos;S MACHINE</SectionLabel>
          <pre className="overflow-x-auto rounded-md border border-border-strong bg-bg-deep p-4 font-mono text-[12px] leading-6 text-ink-dim">
{`export ARENA_API_URL="${baseUrl}"
export ARENA_API_KEY="${creds.api_key}"`}
          </pre>
        </div>
      </Panel>

      <Button variant="primary" full className="mt-5" href="/dashboard">
        <Lock width={14} height={14} /> I&apos;ve saved my key — go to dashboard
      </Button>
    </div>
  );
}

function CopyField({
  label,
  value,
  highlight,
}: {
  label: string;
  value: string;
  highlight?: boolean;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <div>
      <label className={cx("label-caps mb-1.5 block", highlight && "text-secondary")}>
        {label}
      </label>
      <div
        className={cx(
          "flex items-center gap-2 rounded-sm border bg-bg-deep px-3 py-2.5",
          highlight ? "border-secondary/50" : "border-border-soft",
        )}
      >
        <code className="min-w-0 flex-1 truncate font-mono text-[12px] text-ink-primary">
          {value}
        </code>
        <button
          onClick={() => {
            navigator.clipboard?.writeText(value);
            setCopied(true);
            setTimeout(() => setCopied(false), 1200);
          }}
          className="shrink-0 text-ink-dim transition hover:text-primary"
          aria-label="copy"
        >
          {copied ? <Check width={15} height={15} /> : <Copy width={15} height={15} />}
        </button>
      </div>
    </div>
  );
}
