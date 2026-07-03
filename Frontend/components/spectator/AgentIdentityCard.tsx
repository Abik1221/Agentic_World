"use client";

import { Pill, cx, type Tone } from "@/components/ui";
import { AgentAvatar } from "./AgentAvatar";
import {
  STATUS_LABEL,
  profileFor,
  statusTone,
  type AgentProfile,
  type AgentStatus,
} from "@/lib/agentIdentity";

export function AgentIdentityCard({
  profile: base,
  status = "alive",
  speaking,
  voting,
  dead,
  suspicion,
  compact,
  className,
}: {
  profile: AgentProfile | { id: number; name: string; provider?: string; model?: string };
  status?: AgentStatus;
  speaking?: boolean;
  voting?: boolean;
  dead?: boolean;
  suspicion?: number;
  compact?: boolean;
  className?: string;
}) {
  const profile =
    "owner" in base ? base : profileFor(base.id, base.name, { provider: base.provider, model: base.model });
  const tone = statusTone(dead ? "dead" : status) as Tone;

  return (
    <div
      className={cx(
        "rounded-xl border border-border-strong bg-surface-slate/70 p-3 transition-all duration-300",
        speaking && "border-primary-container/60 bg-primary-container/[0.06] shadow-glow-teal",
        voting && "border-secondary/50 animate-vote-pulse",
        dead && "opacity-50",
        className,
      )}
    >
      <div className="flex items-start gap-3">
        <AgentAvatar
          profile={profile}
          size={compact ? "sm" : "md"}
          status={status}
          speaking={speaking}
          voting={voting}
          dead={dead}
        />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="truncate font-display text-base font-semibold text-ink-primary">{profile.name}</h3>
            <Pill tone={tone} className="px-2 py-0.5 text-[9px]">
              {STATUS_LABEL[dead ? "dead" : status]}
            </Pill>
          </div>
          <p className="mt-0.5 truncate font-mono text-[11px] text-ink-faint">
            Owner {profile.owner}
          </p>
          {!compact && (
            <div className="mt-2 flex flex-wrap gap-2">
              <span className="rounded-md border border-border-soft bg-bg-deep/50 px-2 py-0.5 font-mono text-[10px] text-ink-dim">
                #{profile.rank} · {profile.winRate}% WR
              </span>
              <span className="rounded-md border border-primary-container/30 bg-primary-container/10 px-2 py-0.5 font-mono text-[10px] text-primary">
                {profile.personality}
              </span>
            </div>
          )}
          {suspicion != null && suspicion > 0 && !dead && (
            <div className="mt-2">
              <div className="mb-1 flex justify-between font-mono text-[10px] text-ink-faint">
                <span>Suspicion</span>
                <span className="text-secondary">{Math.min(100, suspicion)}%</span>
              </div>
              <div className="h-1.5 overflow-hidden rounded-full bg-bg-deep">
                <div
                  className="h-full rounded-full bg-gradient-to-r from-secondary to-status-error transition-all duration-500"
                  style={{ width: `${Math.min(100, suspicion)}%` }}
                />
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
