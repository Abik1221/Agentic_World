"use client";

import { AgentAvatar } from "./AgentAvatar";
import { profileFor } from "@/lib/agentIdentity";
import { GameCard, SectionLabel, cx } from "@/components/ui";
import { Brain, Flame } from "@/components/icons";

export function GoofPrizeCenter({
  prize,
  pot,
  carryover,
  active,
  subtitle,
  compact,
}: {
  prize: number | string | null;
  pot: number;
  carryover: number;
  active?: boolean;
  subtitle?: string;
  compact?: boolean;
}) {
  const hot = carryover > 0;
  return (
    <div className={cx("flex flex-col items-center justify-center", compact ? "py-2 md:px-4" : "py-6")}>
      <SectionLabel className="mb-4 text-secondary">Current prize</SectionLabel>
      <div className={cx("goof-prize-hero relative", active && "is-live")}>
        <GameCard value={prize ?? "—"} suit="◆" size={compact ? "md" : "lg"} active={active} />
        <div className="absolute -inset-4 -z-10 rounded-3xl bg-[radial-gradient(circle,rgba(240,192,77,0.2),transparent_70%)]" />
      </div>
      <p className={cx("mt-4 font-display font-bold tabular-nums text-primary", compact ? "text-4xl md:text-5xl" : "text-5xl md:text-6xl")}>
        {prize ?? "—"}
        <span className="ml-2 font-mono text-lg text-ink-faint">pts</span>
      </p>
      <div
        className={cx(
          "mt-4 inline-flex items-center gap-2 rounded-lg border px-4 py-2",
          hot ? "border-secondary/50 bg-secondary/10 goof-pot-hot" : "border-border-strong bg-bg-deep/50",
        )}
      >
        {hot ? <Flame width={16} height={16} className="text-secondary" /> : null}
        <span className="font-mono text-base font-semibold tabular-nums">Pot {pot}</span>
        {carryover > 0 && (
          <span className="font-mono text-[11px] text-secondary">+{carryover} carried</span>
        )}
      </div>
      {subtitle && (
        <p className="mt-3 max-w-md text-center font-mono text-[12px] leading-5 text-ink-faint">{subtitle}</p>
      )}
    </div>
  );
}

export function GoofAgentPanel({
  player,
  score,
  handCount,
  leader,
  bid,
  reveal,
  side,
}: {
  player: { id: number; name: string };
  score: number;
  handCount: number;
  leader?: boolean;
  bid?: number;
  reveal?: boolean;
  side: "left" | "right";
}) {
  const profile = profileFor(player.id, player.name);
  return (
    <div
      className={cx(
        "goof-agent-panel flex flex-col rounded-xl border border-border-strong bg-surface-slate/60 p-4 md:p-5",
        side === "left" ? "items-start text-left" : "items-end text-right",
        leader && "border-primary-container/60 shadow-glow-teal",
      )}
    >
      <AgentAvatar profile={profile} size="lg" speaking={leader} />
      <span className="mt-3 font-display text-base font-semibold md:text-lg">{player.name}</span>
      <span className="mt-1 font-mono text-[11px] text-ink-faint">Owner {profile.owner}</span>
      <span className="mt-4 font-mono text-3xl font-bold tabular-nums text-primary md:text-4xl">{score}</span>
      <span className="mt-1 font-mono text-[11px] text-ink-faint">{handCount} cards left</span>
      {reveal && bid != null && (
        <div
          className={cx(
            "mt-4 animate-card-flip rounded-lg border border-tertiary/40 bg-tertiary/10 px-4 py-3",
            side === "right" && "text-right",
          )}
        >
          <span className="label-caps text-tertiary">Played</span>
          <p className="font-mono text-2xl font-bold text-tertiary">{bid}</p>
        </div>
      )}
      {leader && (
        <span className="mt-3 font-mono text-[10px] uppercase tracking-wider text-primary">Leading</span>
      )}
    </div>
  );
}

/** Left agent · center prize · right agent — classic duel layout. */
export function GoofDuelStage({
  players,
  scores,
  hands,
  leader,
  lastBids,
  reveal,
  prize,
  pot,
  carryover,
  active,
  subtitle,
}: {
  players: { id: number; name: string }[];
  scores: Record<number, number>;
  hands: Record<number, number[]>;
  leader?: number | null;
  lastBids?: Record<number, number | undefined>;
  reveal?: boolean;
  prize: number | string | null;
  pot: number;
  carryover: number;
  active?: boolean;
  subtitle?: string;
}) {
  const left = players[0];
  const right = players[1];

  return (
    <div className="goof-duel-stage grid min-h-[360px] items-center gap-4 md:grid-cols-[minmax(140px,1fr)_auto_minmax(140px,1fr)] md:gap-6">
      {left ? (
        <GoofAgentPanel
          player={left}
          score={scores[left.id] ?? 0}
          handCount={hands[left.id]?.length ?? 0}
          leader={leader === left.id}
          bid={lastBids?.[left.id]}
          reveal={reveal}
          side="left"
        />
      ) : (
        <div />
      )}

      <GoofPrizeCenter
        prize={prize}
        pot={pot}
        carryover={carryover}
        active={active}
        subtitle={subtitle}
        compact
      />

      {right ? (
        <GoofAgentPanel
          player={right}
          score={scores[right.id] ?? 0}
          handCount={hands[right.id]?.length ?? 0}
          leader={leader === right.id}
          bid={lastBids?.[right.id]}
          reveal={reveal}
          side="right"
        />
      ) : (
        <div />
      )}
    </div>
  );
}

export function GoofAgentRow({
  players,
  scores,
  hands,
  leader,
  lastBids,
  reveal,
}: {
  players: { id: number; name: string; accent?: string }[];
  scores: Record<number, number>;
  hands: Record<number, number[]>;
  leader?: number | null;
  lastBids?: Record<number, number | undefined>;
  reveal?: boolean;
}) {
  return (
    <div className="goof-agent-row flex gap-3 overflow-x-auto pb-2">
      {players.map((p) => {
        const profile = profileFor(p.id, p.name);
        const isLeader = leader === p.id;
        const bid = lastBids?.[p.id];
        return (
          <div
            key={p.id}
            className={cx(
              "flex min-w-[140px] shrink-0 flex-col items-center rounded-xl border border-border-strong bg-surface-slate/60 p-3",
              isLeader && "border-primary-container/60 shadow-glow-teal",
            )}
          >
            <AgentAvatar profile={profile} size="md" />
            <span className="mt-2 truncate font-display text-sm font-semibold">{p.name}</span>
            <span className="font-mono text-2xl font-bold tabular-nums text-primary">{scores[p.id] ?? 0}</span>
            <span className="font-mono text-[10px] text-ink-faint">{hands[p.id]?.length ?? 0} cards left</span>
            {reveal && bid != null && (
              <span className="mt-2 animate-card-flip rounded-md border border-tertiary/40 bg-tertiary/10 px-2 py-1 font-mono text-sm font-bold text-tertiary">
                Played {bid}
              </span>
            )}
            {isLeader && (
              <span className="mt-1 font-mono text-[9px] uppercase tracking-wider text-primary">Leading</span>
            )}
          </div>
        );
      })}
    </div>
  );
}

export function GoofScoreboardTable({
  players,
  scores,
  hands,
  leader,
  round,
  totalRounds,
}: {
  players: { id: number; name: string }[];
  scores: Record<number, number>;
  hands: Record<number, number[]>;
  leader?: number | null;
  round: number;
  totalRounds: number;
}) {
  const maxScore = Math.max(...Object.values(scores), 1);
  return (
    <div className="overflow-hidden rounded-xl border border-border-strong">
      <div className="grid grid-cols-[auto_1fr_auto_auto_auto] gap-2 border-b border-border-soft bg-bg-deep/60 px-4 py-2 font-mono text-[10px] uppercase tracking-caps text-ink-faint">
        <span>#</span>
        <span>Agent</span>
        <span className="text-right">Score</span>
        <span className="text-right">Cards</span>
        <span className="text-right">Win %</span>
      </div>
      {[...players]
        .sort((a, b) => (scores[b.id] ?? 0) - (scores[a.id] ?? 0))
        .map((p, i) => {
          const profile = profileFor(p.id, p.name, { rank: i + 1 });
          const score = scores[p.id] ?? 0;
          const winProb = Math.round((score / maxScore) * 100);
          return (
            <div
              key={p.id}
              className={cx(
                "grid grid-cols-[auto_1fr_auto_auto_auto] items-center gap-2 border-b border-border-soft/50 px-4 py-3 last:border-0",
                leader === p.id && "bg-primary-container/[0.04]",
              )}
            >
              <span className="font-mono text-[11px] text-ink-faint">{i + 1}</span>
              <div className="flex items-center gap-2 min-w-0">
                <AgentAvatar profile={profile} size="sm" />
                <span className="truncate font-display text-sm font-semibold">{p.name}</span>
              </div>
              <span className="font-mono text-sm font-semibold tabular-nums text-primary">{score}</span>
              <span className="font-mono text-[12px] tabular-nums text-ink-dim">{hands[p.id]?.length ?? 0}</span>
              <span className="font-mono text-[12px] tabular-nums text-ink-dim">{winProb}%</span>
            </div>
          );
        })}
      <div className="border-t border-border-soft px-4 py-2 font-mono text-[10px] text-ink-faint">
        Round {round}/{totalRounds}
      </div>
    </div>
  );
}

export function ThoughtSummary({ agent, card, text }: { agent: string; card: number; text: string }) {
  return (
    <div className="rounded-xl border border-tertiary/30 bg-tertiary/5 p-4">
      <div className="flex items-center gap-2">
        <Brain width={16} height={16} className="text-tertiary" />
        <SectionLabel className="text-tertiary">Why did {agent} play {card}?</SectionLabel>
      </div>
      <p className="mt-2 text-[15px] leading-relaxed text-ink-primary">{text}</p>
    </div>
  );
}
