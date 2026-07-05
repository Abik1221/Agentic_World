"use client";

import * as React from "react";
import { Settings2, ShieldCheck } from "lucide-react";
import { limits as defaults, userAgent as fallbackAgent } from "@/lib/mock";
import { fetchLimits, updateConfig } from "@/lib/api";
import { getSession } from "@/lib/session";
import { cn } from "@/lib/cn";
import { Badge, Button, Card, CardHeader, PageHeader } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";

const fields: { key: keyof typeof defaults; label: string; help: string; min: number; max: number; step: number }[] = [
  { key: "coin_limit_per_match", label: "Coin limit / match", help: "Max coins riskable in one match", min: 0, max: 1000, step: 10 },
  { key: "max_bid", label: "Max bid", help: "Max single bid (≤ coin limit / match)", min: 0, max: 1000, step: 10 },
  { key: "min_wallet_balance", label: "Min wallet balance", help: "Reserve that can't be staked", min: 0, max: 1000, step: 10 },
  { key: "daily_loss_limit", label: "Daily loss limit", help: "Stop after losing this much today", min: 0, max: 5000, step: 50 },
  { key: "session_loss_limit", label: "Session loss limit", help: "Stop after losing this much this session", min: 0, max: 10000, step: 50 },
  { key: "max_concurrent_matches", label: "Max concurrent matches", help: "How many matches at once", min: 1, max: 10, step: 1 },
  { key: "cooldown_losses", label: "Cooldown after N losses", help: "Trigger cooldown after this many losses", min: 0, max: 10, step: 1 },
];

export default function StrategyPage() {
  const [cfg, setCfg] = React.useState({ ...defaults });
  const [agent, setAgent] = React.useState({ id: fallbackAgent.id, name: fallbackAgent.name });
  const [status, setStatus] = React.useState<"idle" | "saving" | "saved" | "error">("idle");
  const [error, setError] = React.useState<string | null>(null);
  const set = (k: keyof typeof defaults, v: number | boolean) => setCfg((c) => ({ ...c, [k]: v }));
  const invalid = cfg.max_bid > cfg.coin_limit_per_match;

  React.useEffect(() => {
    const session = getSession();
    if (session.agentId || session.agentName) {
      setAgent({ id: session.agentId ?? fallbackAgent.id, name: session.agentName ?? fallbackAgent.name });
    }
    fetchLimits(session).then((live) => setCfg({ ...live }));
  }, []);

  async function commit() {
    setStatus("saving");
    setError(null);
    try {
      await updateConfig(getSession(), agent.id, cfg);
      setStatus("saved");
      setTimeout(() => setStatus("idle"), 2000);
    } catch (e) {
      setStatus("error");
      setError((e as Error)?.message ?? "Failed to save config.");
    }
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title="Strategy"
        subtitle={`Owner-only execution guardrails for ${agent.name} — an agent key can never change these`}
        actions={<Badge tone="brand">{agent.id}</Badge>}
      />
      <SectionTabs />

      <div className="grid gap-4 lg:grid-cols-[1.6fr_1fr]">
        <Card className="p-6">
          <div className="space-y-6">
            {fields.map((f) => (
              <div key={f.key}>
                <div className="mb-2 flex items-end justify-between">
                  <div>
                    <span className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">{f.label}</span>
                    <p className="mt-0.5 font-mono text-[11px] text-fg-muted/70">{f.help}</p>
                  </div>
                  <span className="font-mono text-lg font-semibold tabular-nums text-brand">{cfg[f.key] as number}</span>
                </div>
                <input
                  type="range"
                  min={f.min}
                  max={f.max}
                  step={f.step}
                  value={cfg[f.key] as number}
                  onChange={(e) => set(f.key, Number(e.target.value))}
                  className="console-range w-full"
                />
              </div>
            ))}

            <div className="flex items-center justify-between border-t border-line pt-5">
              <div>
                <span className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">Auto-join queue</span>
                <p className="mt-0.5 font-mono text-[11px] text-fg-muted/70">Re-queue automatically after each match (reserved)</p>
              </div>
              <button
                onClick={() => set("auto_join", !cfg.auto_join)}
                className={cn(
                  "relative h-6 w-11 rounded-full border transition",
                  cfg.auto_join ? "border-brand bg-brand/30" : "border-line bg-panel-2",
                )}
              >
                <span className={cn("absolute top-0.5 h-4 w-4 rounded-full transition-all", cfg.auto_join ? "left-6 bg-brand" : "left-0.5 bg-fg-muted")} />
              </button>
            </div>
          </div>

          <div className="mt-7 flex flex-wrap items-center gap-3 border-t border-line pt-6">
            <Button onClick={commit} disabled={status === "saving" || invalid}>
              <Settings2 className="h-4 w-4" />
              {status === "saving" ? "Committing…" : "Commit config"}
            </Button>
            <Button variant="outline" onClick={() => setCfg({ ...defaults })}>
              Reset defaults
            </Button>
            {status === "saved" && <span className="font-mono text-[12px] text-ok">✓ Config saved</span>}
            {status === "error" && <span className="font-mono text-[12px] text-danger">✕ {error}</span>}
          </div>
        </Card>

        <div className="space-y-4">
          <Card className="p-5">
            <CardHeader title="Risk Envelope" subtitle="Enforced server-side on every move" />
            <div className="mt-4 space-y-3 font-mono text-sm">
              <Row k="Per-match exposure" v={`${cfg.coin_limit_per_match} CRD`} />
              <Row k="Largest single bid" v={`${cfg.max_bid} CRD`} warn={invalid} />
              <Row k="Protected reserve" v={`${cfg.min_wallet_balance} CRD`} />
              <Row k="Daily stop-loss" v={`${cfg.daily_loss_limit} CRD`} />
              <Row k="Parallel matches" v={`×${cfg.max_concurrent_matches}`} />
              <Row k="Cooldown trigger" v={`${cfg.cooldown_losses} losses / ${cfg.cooldown_seconds}s`} />
            </div>
            {invalid && (
              <p className="mt-4 rounded-md border border-danger/40 bg-danger/10 px-3 py-2 font-mono text-[11px] text-danger">
                ✕ max_bid must be ≤ coin_limit_per_match — server returns 409.
              </p>
            )}
          </Card>

          <Card className="p-5">
            <div className="flex items-start gap-3">
              <ShieldCheck className="h-5 w-5 shrink-0 text-brand" />
              <div>
                <div className="text-sm font-medium text-fg">Owner-scope firewall</div>
                <p className="mt-1 text-sm text-fg-muted">A violated limit aborts the action with a 409 carrying the blocking limit code.</p>
              </div>
            </div>
          </Card>
        </div>
      </div>

      <style>{`
        .console-range { -webkit-appearance: none; appearance: none; height: 6px; border-radius: 9999px; background: rgb(var(--k-panel-2)); outline: none; }
        .console-range::-webkit-slider-thumb { -webkit-appearance: none; appearance: none; width: 16px; height: 16px; border-radius: 9999px; background: rgb(var(--k-brand)); box-shadow: 0 0 8px rgba(99,102,241,0.5); cursor: pointer; border: none; }
        .console-range::-moz-range-thumb { width: 16px; height: 16px; border-radius: 9999px; background: rgb(var(--k-brand)); box-shadow: 0 0 8px rgba(99,102,241,0.5); cursor: pointer; border: none; }
      `}</style>
    </div>
  );
}

function Row({ k, v, warn }: { k: string; v: string; warn?: boolean }) {
  return (
    <div className="flex items-center justify-between border-b border-line pb-2">
      <span className="text-fg-muted">{k}</span>
      <span className={warn ? "text-danger" : "text-fg"}>{v}</span>
    </div>
  );
}
