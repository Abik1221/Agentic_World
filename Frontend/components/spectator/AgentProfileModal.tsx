"use client";

import { useEffect } from "react";
import { AgentAvatar } from "./AgentAvatar";
import { profileFor } from "@/lib/agentIdentity";
import { cx } from "@/components/ui";

export interface AgentModalData {
  id: number;
  name: string;
  owner?: string;
  model?: string;
  role?: string;
  alive: boolean;
  suspicion: number;
  status: string;
}

/** Deterministic pseudo-stat in [0,1) so a profile is stable across renders. */
function h(n: number): number {
  const x = Math.sin(n * 99.73) * 10000;
  return x - Math.floor(x);
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: string }) {
  return (
    <div className="rounded-md border border-border-soft bg-bg-deep/50 px-3 py-2">
      <div className={cx("font-display text-lg font-semibold tabular-nums", tone ?? "text-ink-primary")}>{value}</div>
      <div className="mt-0.5 font-mono text-[9px] uppercase tracking-caps text-ink-faint">{label}</div>
    </div>
  );
}

export function AgentProfileModal({ agent, onClose }: { agent: AgentModalData | null; onClose: () => void }) {
  useEffect(() => {
    if (!agent) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [agent, onClose]);

  if (!agent) return null;

  const profile = profileFor(agent.id, agent.name);
  const rating = 2400 + Math.round(h(agent.id) * 640);
  const games = 4800 + Math.round(h(agent.id + 1) * 9200);
  const winrate = 56 + Math.round(h(agent.id + 2) * 30);
  const trust = Math.max(6, Math.min(99, 100 - agent.suspicion));
  const styleLabel = profile.personality ?? "Strategist";
  const decisions = Array.from({ length: 20 }, (_, i) => h(agent.id * 7 + i));

  return (
    <div
      className="modal-backdrop-in fixed inset-0 z-50 flex items-center justify-center bg-black/65 p-4 backdrop-blur-sm"
      onClick={onClose}
    >
      <div className="modal-in glass w-full max-w-md p-6" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-start gap-4">
          <AgentAvatar profile={profile} size="xl" status={agent.alive ? "alive" : "dead"} dead={!agent.alive} />
          <div className="min-w-0 flex-1">
            <h3 className="font-display text-xl font-bold text-ink-primary">{agent.name}</h3>
            <p className="font-mono text-[11px] text-ink-faint">
              {agent.owner ?? profile.owner} · {agent.model ?? "GPT-5.5"}
            </p>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <span className="rounded-full border border-primary-container/40 px-2 py-0.5 font-mono text-[9px] uppercase tracking-caps text-primary">
                {styleLabel}
              </span>
              <span
                className={cx(
                  "rounded-full border px-2 py-0.5 font-mono text-[9px] uppercase tracking-caps",
                  agent.alive ? "border-tertiary/40 text-tertiary" : "border-border-strong text-ink-faint",
                )}
              >
                {agent.status}
              </span>
            </div>
          </div>
          <button onClick={onClose} className="shrink-0 text-ink-faint transition hover:text-ink-primary" aria-label="Close">
            ✕
          </button>
        </div>

        <div className="mt-5 grid grid-cols-3 gap-2">
          <Stat label="Rating" value={String(rating)} tone="text-primary" />
          <Stat label="Games" value={games.toLocaleString("en-US")} />
          <Stat label="Win rate" value={`${winrate}%`} tone="text-secondary" />
        </div>

        <div className="mt-4">
          <div className="flex items-center justify-between font-mono text-[10px] uppercase tracking-caps">
            <span className="text-ink-faint">Trust score</span>
            <span className={trust >= 50 ? "text-primary" : "text-status-error"}>{trust}%</span>
          </div>
          <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-bg-deep">
            <div
              className={cx("conf-fill h-full rounded-full", trust >= 50 ? "bg-primary" : "bg-status-error")}
              style={{ width: `${trust}%` }}
            />
          </div>
        </div>

        <div className="mt-4">
          <div className="mb-2 font-mono text-[10px] uppercase tracking-caps text-ink-faint">Last 20 decisions</div>
          <div className="flex flex-wrap gap-1">
            {decisions.map((d, i) => (
              <span
                key={i}
                title={`Decision ${i + 1}`}
                className="h-4 w-4 rounded-[3px]"
                style={{
                  backgroundColor:
                    d > 0.66
                      ? "rgb(var(--c-primary) / 0.85)"
                      : d > 0.33
                        ? "rgb(var(--c-secondary) / 0.8)"
                        : "rgb(var(--c-status-error) / 0.8)",
                }}
              />
            ))}
          </div>
        </div>

        <button onClick={onClose} className="btn-neutral mt-5 w-full">
          Close
        </button>
      </div>
    </div>
  );
}
