"use client";

import { AgentIdentityCard } from "./AgentIdentityCard";
import { profileFor } from "@/lib/agentIdentity";
import { SectionLabel } from "@/components/ui";

/** Suspicion leaderboard for spectators — PDF spec. */
export function SuspicionBoard({
  entries,
}: {
  entries: { id: number; name: string; suspicion: number }[];
}) {
  const sorted = [...entries].sort((a, b) => b.suspicion - a.suspicion).slice(0, 5);

  return (
    <div className="rounded-xl border border-border-strong bg-surface-slate/60 p-4">
      <SectionLabel className="mb-3 text-secondary">Suspicion meter</SectionLabel>
      <div className="space-y-2">
        {sorted.map((e) => (
          <div key={e.id} className="flex items-center justify-between gap-2">
            <span className="truncate font-display text-sm text-ink-primary">{e.name}</span>
            <span className="shrink-0 font-mono text-sm font-semibold tabular-nums text-secondary">
              {Math.min(100, e.suspicion)}%
            </span>
          </div>
        ))}
        {sorted.length === 0 && (
          <p className="font-mono text-[11px] text-ink-faint">No suspects yet</p>
        )}
      </div>
    </div>
  );
}

export function AgentLeaderboard({
  agents,
}: {
  agents: { id: number; name: string; score?: number; label?: string }[];
}) {
  return (
    <div className="rounded-xl border border-border-strong bg-surface-slate/60 p-4">
      <SectionLabel className="mb-3 text-primary">Agent leaderboard</SectionLabel>
      <div className="space-y-2">
        {agents.map((a, i) => (
          <AgentIdentityCard
            key={a.id}
            profile={profileFor(a.id, a.name, { rank: i + 1 })}
            compact
            className="!p-2"
          />
        ))}
      </div>
    </div>
  );
}
