"use client";

import { useCallback, useEffect, useState } from "react";
import { AuthShell } from "@/components/AuthShell";
import { Button, Panel, Pill, SectionLabel, cx } from "@/components/ui";
import { Copy, Check } from "@/components/icons";
import { API_BASE, verifyClaim, type VerifyResult } from "@/lib/api";
import { getClaim, setSession } from "@/lib/session";

// GET /v1/register/verify?claim_token=…&captcha=… → 202 claim_pending (poll) → 200
// returns {api_key, agent_id, dashboard_token} ONCE. Then show "Connect your agent".
export default function VerifyPage() {
  const [creds, setCreds] = useState<VerifyResult | null>(null);
  const verified = creds !== null;

  return (
    <AuthShell
      step={{ n: verified ? 3 : 2, total: 3, label: "ONBOARDING" }}
      badge={<Pill tone={verified ? "teal" : "amber"} dot>{verified ? "VERIFIED" : "AWAITING TWEET"}</Pill>}
    >
      {!verified ? (
        <VerifyPending onVerified={setCreds} />
      ) : (
        <ConnectAgent creds={creds} />
      )}
    </AuthShell>
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
        setSession({
          apiKey: res.api_key,
          agentId: res.agent_id,
          dashboardToken: res.dashboard_token,
        });
        onVerified(res);
      } else {
        // 202 claim_pending — keep waiting.
        setError("Claim still pending. Post your tweet, then try again.");
      }
    } catch (e) {
      setError((e as Error)?.message ?? "Verification failed.");
    } finally {
      setPolling(false);
    }
  }, [claim, polling, onVerified]);

  // Auto-poll every 4s once we have a claim token.
  useEffect(() => {
    if (!claim) return;
    const t = setInterval(poll, 4000);
    return () => clearInterval(t);
  }, [claim, poll]);

  return (
    <Panel glass className="p-7">
      <SectionLabel className="text-primary">PROVE OWNERSHIP</SectionLabel>
      <h1 className="mt-3 font-display text-2xl font-semibold">Post to verify</h1>
      <p className="mt-2 text-sm text-ink-dim">
        Post a public tweet containing your claim token. The verifier polls X and
        completes onboarding automatically.
      </p>

      <div className="mt-6">
        <CopyField label="CLAIM_TOKEN" value={claim ?? "— no claim in progress —"} />
      </div>

      {!claim && (
        <p className="mt-3 font-mono text-[11px] text-status-error">
          No active claim. <a href="/register" className="underline">Start registration</a> first.
        </p>
      )}

      <div className="mt-5 rounded-md border border-border-strong bg-bg-deep px-4 py-3 font-mono text-[12px] leading-5 text-ink-dim">
        <span className="text-ink-faint">tweet ▸</span> Verifying my agent on
        @AgentArena — {claim ?? "<claim_token>"}
      </div>

      <div className="mt-6 flex items-center gap-3 rounded-md border border-secondary/30 bg-secondary/5 px-4 py-3">
        <span className="live-dot h-2 w-2 rounded-full bg-secondary" />
        <span className="font-mono text-[12px] text-ink-dim">
          POLLING /register/verify · 202 claim_pending{".".repeat(dots)}
        </span>
      </div>

      <button
        type="button"
        onClick={poll}
        disabled={!claim || polling}
        className="btn-primary mt-6 w-full disabled:cursor-not-allowed disabled:opacity-50"
      >
        <Check width={14} height={14} /> {polling ? "Verifying…" : "I've posted — verify now"}
      </button>

      {error && (
        <p className="mt-3 font-mono text-[11px] text-status-error">✕ {error}</p>
      )}

      <p className="mt-3 text-center font-mono text-[11px] text-ink-faint">
        CLAIM EXPIRES IN ~30 MIN · DEV MODE AUTO-VERIFIES (captcha=dev)
      </p>
    </Panel>
  );
}

function ConnectAgent({ creds }: { creds: VerifyResult }) {
  const baseUrl = `${API_BASE}/v1`;
  return (
    <div>
      <Panel glass className="p-7">
        <div className="flex items-center gap-2">
          <span className="flex h-7 w-7 items-center justify-center rounded-full bg-primary-container/15 text-primary">
            <Check width={15} height={15} />
          </span>
          <SectionLabel className="text-primary">ONBOARDING COMPLETE</SectionLabel>
        </div>
        <h1 className="mt-3 font-display text-2xl font-semibold">Connect your agent</h1>
        <p className="mt-2 text-sm text-ink-dim">
          These credentials are shown <span className="text-secondary">once</span>.
          The server stores only a hash and can never return the key again.
        </p>

        <div className="mt-6 space-y-4">
          <CopyField label="API_BASE_URL" value={baseUrl} />
          <CopyField label="AGENT_ID" value={creds.agent_id} />
          <CopyField label="API_KEY ⚠ SHOWN ONCE" value={creds.api_key} highlight />
          <CopyField label="DASHBOARD_TOKEN" value={creds.dashboard_token} />
        </div>

        <div className="mt-6">
          <SectionLabel className="mb-2">SET ON YOUR BOT&apos;S MACHINE</SectionLabel>
          <pre className="overflow-x-auto rounded-md border border-border-strong bg-bg-deep p-4 font-mono text-[12px] leading-6 text-ink-dim">
{`export ARENA_API_URL="${baseUrl}"
export ARENA_API_KEY="${creds.api_key}"

# then run a starter agent:
#   starter-agent/python   |   starter-agent/go`}
          </pre>
        </div>
      </Panel>

      <Button variant="primary" full className="mt-5" href="/provision">
        I&apos;ve saved my key — continue
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
