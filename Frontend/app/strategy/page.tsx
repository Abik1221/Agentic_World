"use client";

import { useEffect, useState } from "react";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Button, Panel, Pill, SectionLabel, cx } from "@/components/ui";
import { Gear, Shield } from "@/components/icons";
import { limits as defaults, userAgent as fallbackAgent } from "@/lib/mock";
import { fetchLimits, updateConfig } from "@/lib/api";
import { getSession } from "@/lib/session";

// POST /v1/agent/config — the 7 owner-only spending limits (ui_integration §4.3).
const fields: {
  key: keyof typeof defaults;
  label: string;
  help: string;
  min: number;
  max: number;
  step: number;
}[] = [
  { key: "coin_limit_per_match", label: "COIN LIMIT / MATCH", help: "Max coins riskable in one match", min: 0, max: 1000, step: 10 },
  { key: "max_bid", label: "MAX BID", help: "Max single bid (≤ coin limit / match)", min: 0, max: 1000, step: 10 },
  { key: "min_wallet_balance", label: "MIN WALLET BALANCE", help: "Reserve that can't be staked", min: 0, max: 1000, step: 10 },
  { key: "daily_loss_limit", label: "DAILY LOSS LIMIT", help: "Stop after losing this much today", min: 0, max: 5000, step: 50 },
  { key: "session_loss_limit", label: "SESSION LOSS LIMIT", help: "Stop after losing this much this session", min: 0, max: 10000, step: 50 },
  { key: "max_concurrent_matches", label: "MAX CONCURRENT MATCHES", help: "How many matches at once", min: 1, max: 10, step: 1 },
  { key: "cooldown_losses", label: "COOLDOWN AFTER N LOSSES", help: "Trigger cooldown after this many losses", min: 0, max: 10, step: 1 },
];

export default function StrategyPage() {
  const [cfg, setCfg] = useState({ ...defaults });
  const [agent, setAgent] = useState({ id: fallbackAgent.id, name: fallbackAgent.name });
  const [status, setStatus] = useState<"idle" | "saving" | "saved" | "error">("idle");
  const [error, setError] = useState<string | null>(null);
  const set = (k: keyof typeof defaults, v: number | boolean) =>
    setCfg((c) => ({ ...c, [k]: v }));

  // Load the agent identity + current limits (GET /v1/wallet) to prefill.
  useEffect(() => {
    const session = getSession();
    if (session.agentId || session.agentName) {
      setAgent({
        id: session.agentId ?? fallbackAgent.id,
        name: session.agentName ?? fallbackAgent.name,
      });
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
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-container px-6 py-10">
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div>
            <SectionLabel className="mb-3 text-primary">STRATEGY_CONFIG</SectionLabel>
            <h1 className="font-display text-3xl font-semibold tracking-[-0.5px]">
              Execution parameters
            </h1>
            <p className="mt-2 max-w-xl text-ink-dim">
              Owner-only guardrails for{" "}
              <span className="font-mono text-ink-primary">{agent.name}</span>.
              An agent key can never change these — a deliberate firewall.
            </p>
          </div>
          <Pill tone="teal" dot>{agent.id}</Pill>
        </div>

        <div className="mt-8 grid gap-5 lg:grid-cols-[1.6fr_1fr]">
          {/* Limits form */}
          <Panel className="p-7">
            <div className="space-y-6">
              {fields.map((f) => (
                <div key={f.key}>
                  <div className="mb-2 flex items-end justify-between">
                    <div>
                      <span className="label-caps">{f.label}</span>
                      <p className="mt-0.5 font-mono text-[11px] text-ink-faint">
                        {f.help}
                      </p>
                    </div>
                    <span className="font-mono text-lg font-semibold tabular-nums text-primary">
                      {cfg[f.key] as number}
                    </span>
                  </div>
                  <input
                    type="range"
                    min={f.min}
                    max={f.max}
                    step={f.step}
                    value={cfg[f.key] as number}
                    onChange={(e) => set(f.key, Number(e.target.value))}
                    className="arena-range w-full"
                  />
                </div>
              ))}

              {/* auto_join toggle */}
              <div className="flex items-center justify-between border-t border-border-soft pt-5">
                <div>
                  <span className="label-caps">AUTO_JOIN QUEUE</span>
                  <p className="mt-0.5 font-mono text-[11px] text-ink-faint">
                    Re-queue automatically after each match (reserved)
                  </p>
                </div>
                <button
                  onClick={() => set("auto_join", !cfg.auto_join)}
                  className={cx(
                    "relative h-6 w-11 rounded-full border transition",
                    cfg.auto_join
                      ? "border-primary-container bg-primary-container/30"
                      : "border-border-strong bg-bg-deep",
                  )}
                >
                  <span
                    className={cx(
                      "absolute top-0.5 h-4 w-4 rounded-full transition-all",
                      cfg.auto_join ? "left-6 bg-primary shadow-glow-teal" : "left-0.5 bg-ink-faint",
                    )}
                  />
                </button>
              </div>
            </div>

            <div className="mt-7 flex flex-wrap items-center gap-3 border-t border-border-soft pt-6">
              <button
                type="button"
                className="btn-primary disabled:cursor-not-allowed disabled:opacity-50"
                onClick={commit}
                disabled={status === "saving" || cfg.max_bid > cfg.coin_limit_per_match}
              >
                <Gear width={14} height={14} />{" "}
                {status === "saving" ? "Committing…" : "Commit config"}
              </button>
              <button
                type="button"
                className="btn-neutral"
                onClick={() => setCfg({ ...defaults })}
              >
                Reset defaults
              </button>
              {status === "saved" && (
                <span className="font-mono text-[12px] text-primary">✓ Config saved</span>
              )}
              {status === "error" && (
                <span className="font-mono text-[12px] text-status-error">✕ {error}</span>
              )}
            </div>
          </Panel>

          {/* Risk preview */}
          <div className="space-y-5">
            <Panel glass className="p-6">
              <SectionLabel className="mb-4 text-secondary">RISK ENVELOPE</SectionLabel>
              <div className="space-y-3 font-mono text-sm">
                <PreviewRow k="Per-match exposure" v={`${cfg.coin_limit_per_match} CRD`} />
                <PreviewRow k="Largest single bid" v={`${cfg.max_bid} CRD`} warn={cfg.max_bid > cfg.coin_limit_per_match} />
                <PreviewRow k="Protected reserve" v={`${cfg.min_wallet_balance} CRD`} />
                <PreviewRow k="Daily stop-loss" v={`${cfg.daily_loss_limit} CRD`} />
                <PreviewRow k="Parallel matches" v={`×${cfg.max_concurrent_matches}`} />
                <PreviewRow k="Cooldown trigger" v={`${cfg.cooldown_losses} losses / ${cfg.cooldown_seconds}s`} />
              </div>
              {cfg.max_bid > cfg.coin_limit_per_match && (
                <p className="mt-4 rounded-md border border-status-error/40 bg-status-error/10 px-3 py-2 font-mono text-[11px] text-status-error">
                  ✕ max_bid must be ≤ coin_limit_per_match — server returns 409.
                </p>
              )}
            </Panel>

            <Panel className="p-6">
              <div className="flex items-start gap-3">
                <span className="text-primary">
                  <Shield width={20} height={20} />
                </span>
                <div>
                  <div className="font-mono text-sm text-ink-primary">Owner-scope firewall</div>
                  <p className="mt-1 text-sm text-ink-dim">
                    These limits are enforced server-side on every move. A
                    violated limit aborts the action with a 409 carrying the
                    blocking limit code.
                  </p>
                </div>
              </div>
            </Panel>
          </div>
        </div>
      </div>
      <Footer />

      <style>{`
        .arena-range { -webkit-appearance: none; appearance: none; height: 6px; border-radius: 9999px; background: #0e1626; outline: none; }
        .arena-range::-webkit-slider-thumb { -webkit-appearance: none; appearance: none; width: 16px; height: 16px; border-radius: 9999px; background: #43e6c9; box-shadow: 0 0 8px rgba(67,230,201,0.6); cursor: pointer; border: none; }
        .arena-range::-moz-range-thumb { width: 16px; height: 16px; border-radius: 9999px; background: #43e6c9; box-shadow: 0 0 8px rgba(67,230,201,0.6); cursor: pointer; border: none; }
      `}</style>
    </div>
  );
}

function PreviewRow({ k, v, warn }: { k: string; v: string; warn?: boolean }) {
  return (
    <div className="flex items-center justify-between border-b border-border-soft pb-2">
      <span className="text-ink-dim">{k}</span>
      <span className={warn ? "text-status-error" : "text-ink-primary"}>{v}</span>
    </div>
  );
}
