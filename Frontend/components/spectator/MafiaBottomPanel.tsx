"use client";

import { ReactNode } from "react";
import { SectionLabel, cx } from "@/components/ui";
import { profileFor } from "@/lib/agentIdentity";
import { AgentAvatar } from "./AgentAvatar";

export function MafiaBottomPanel({
  timeline,
  alive,
  eliminated,
  votes,
  stats,
}: {
  timeline: ReactNode;
  alive: { id: number; name: string; speaking?: boolean; suspected?: boolean }[];
  eliminated: { id: number; name: string }[];
  votes?: ReactNode;
  stats: ReactNode;
}) {
  return (
    <div className="space-y-4">
      <div>
        <SectionLabel className="mb-2 text-ink-faint">Match timeline</SectionLabel>
        {timeline}
      </div>

      <div className="grid gap-4 lg:grid-cols-4">
        <div className="lg:col-span-1">
          <SectionLabel className="mb-2 text-primary">Alive ({alive.length})</SectionLabel>
          <div className="flex flex-wrap gap-2">
            {alive.map((a) => (
              <AgentChip key={a.id} {...a} />
            ))}
          </div>
        </div>

        <div className="lg:col-span-1">
          <SectionLabel className="mb-2 text-ink-faint">Eliminated ({eliminated.length})</SectionLabel>
          <div className="flex flex-wrap gap-2">
            {eliminated.length === 0 ? (
              <span className="font-mono text-[11px] text-ink-faint">None yet</span>
            ) : (
              eliminated.map((a) => (
                <span
                  key={a.id}
                  className="inline-flex items-center gap-1.5 rounded-full border border-border-soft bg-surface-container/60 px-2.5 py-1 opacity-60"
                >
                  <AgentAvatar profile={profileFor(a.id, a.name)} size="sm" dead />
                  <span className="font-mono text-[10px] text-ink-faint line-through">{a.name.split("_")[0]}</span>
                </span>
              ))
            )}
          </div>
        </div>

        <div className="lg:col-span-1">
          <SectionLabel className="mb-2 text-status-error">Votes</SectionLabel>
          {votes ?? <span className="font-mono text-[11px] text-ink-faint">No active vote</span>}
        </div>

        <div className="lg:col-span-1">{stats}</div>
      </div>
    </div>
  );
}

function AgentChip({
  id,
  name,
  speaking,
  suspected,
}: {
  id: number;
  name: string;
  speaking?: boolean;
  suspected?: boolean;
}) {
  const profile = profileFor(id, name);
  return (
    <span
      className={cx(
        "inline-flex items-center gap-1.5 rounded-full border px-2 py-1 transition",
        speaking && "border-primary/40 bg-primary/5 shadow-glow-teal",
        suspected && !speaking && "border-secondary/40 bg-secondary/5",
        !speaking && !suspected && "border-border-soft bg-surface-slate",
      )}
    >
      <AgentAvatar profile={profile} size="sm" speaking={speaking} status={speaking ? "speaking" : suspected ? "suspected" : "alive"} />
      <span className="max-w-[64px] truncate font-mono text-[10px] text-ink-primary">{name.split("_")[0]}</span>
    </span>
  );
}
