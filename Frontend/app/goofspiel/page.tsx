"use client";

import { Suspense, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "next/navigation";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, Pill, SectionLabel, Stat, GameCard, cx, type Tone } from "@/components/ui";
import {
  Brain,
  Crosshair,
  Trophy,
  Layers,
  Flame,
  Equals,
  Coin,
  Cpu,
  Play,
  Pause,
  ChevronLeft,
  ChevronRight,
  Restart,
} from "@/components/icons";
import {
  deriveGoofStateFrom,
  goofMatchMeta,
  goofLeader,
  type GoofPhase,
  type GoofState,
  type GoofPlayer,
  type GoofFeedItem,
  type GoofRoundResult,
} from "@/lib/goofspiel";
import { useGoofFeed, type GoofFeedStatus } from "@/lib/useGoofFeed";
import { SpectatorBroadcast, CommentaryStrip } from "@/components/spectator/SpectatorBroadcast";
import {
  GoofDuelStage,
  GoofScoreboardTable,
  ThoughtSummary,
} from "@/components/spectator/GoofBroadcast";
import { AgentLeaderboard } from "@/components/spectator/SidePanels";

type Mode = "connecting" | "live" | "replay";

function byId(players: GoofPlayer[], id: number | null | undefined): GoofPlayer | undefined {
  return id == null ? undefined : players.find((p) => p.id === id);
}

const PHASE_META: Record<GoofPhase, { label: string; tone: Tone; Icon: typeof Brain }> = {
  reveal: { label: "AGENTS DECIDING", tone: "blue", Icon: Brain },
  showdown: { label: "SHOWDOWN", tone: "amber", Icon: Crosshair },
  final: { label: "MATCH COMPLETE", tone: "teal", Icon: Trophy },
};

function accentText(tone: Tone): string {
  return tone === "teal"
    ? "text-primary"
    : tone === "blue"
      ? "text-tertiary"
      : tone === "amber"
        ? "text-secondary"
        : tone === "red"
          ? "text-status-error"
          : "text-ink-dim";
}

export default function GoofspielPage() {
  return (
    <Suspense
      fallback={
        <div className="min-h-screen">
          <TopNav />
          <div className="mx-auto max-w-container px-6 py-20 font-mono text-sm text-ink-faint">
            Connecting to the strategy theater…
          </div>
        </div>
      }
    >
      <GoofspielConsole />
    </Suspense>
  );
}

function GoofspielConsole() {
  const searchParams = useSearchParams();
  const preferredMatch = searchParams.get("match");
  const feed = useGoofFeed(preferredMatch);
  const events = feed.events;
  const players = feed.players;
  const total = events.length;
  const mode: Mode = feed.live ? "live" : feed.status === "fallback" ? "replay" : "connecting";

  const [index, setIndex] = useState(0);
  const [playing, setPlaying] = useState(true);
  const [speed, setSpeed] = useState<1 | 2 | 4>(1);
  const [following, setFollowing] = useState(true);
  const [view, setView] = useState<GoofView>("table");
  const [elapsed, setElapsed] = useState(154);

  useEffect(() => {
    const t = setInterval(() => setElapsed((e) => e + 1), 1000);
    return () => clearInterval(t);
  }, []);

  // Replay auto-advance (scripted fallback only).
  useEffect(() => {
    if (mode !== "replay" || !playing) return;
    if (index >= total - 1) {
      setPlaying(false);
      return;
    }
    const ms = speed === 4 ? 700 : speed === 2 ? 1300 : 2300;
    const t = setTimeout(() => setIndex((i) => Math.min(total - 1, i + 1)), ms);
    return () => clearTimeout(t);
  }, [mode, playing, speed, index, total]);

  // Live: pin the playhead to the newest event unless the viewer scrubbed back.
  useEffect(() => {
    if (mode === "live" && following) setIndex(Math.max(0, total - 1));
  }, [mode, following, total]);

  // Clamp when the buffer shrinks (source switches).
  useEffect(() => {
    setIndex((i) => Math.min(i, Math.max(0, total - 1)));
  }, [total]);

  const stepBack = () => {
    setFollowing(false);
    setIndex((i) => Math.max(0, i - 1));
  };
  const stepFwd = () => {
    setFollowing(false);
    setIndex((i) => Math.min(total - 1, i + 1));
  };
  const jumpLive = () => {
    setFollowing(true);
    setIndex(Math.max(0, total - 1));
  };

  const state = useMemo(() => deriveGoofStateFrom(events, index), [events, index]);
  const gameOver = state.winner != null;
  const phase = PHASE_META[state.phase];
  const leader = goofLeader(state.scores, players);
  const isPot = state.carryover > 0 || (state.phase === "showdown" && state.lastTie);

  const lastThought = useMemo(() => {
    for (let i = state.feed.length - 1; i >= 0; i--) {
      const item = state.feed[i];
      if (item.type === "think" && item.from != null) {
        const p = byId(players, item.from);
        const card = state.lastBid[item.from];
        return {
          agent: p?.name ?? `Agent ${item.from}`,
          card: card ?? 0,
          text: item.text,
        };
      }
    }
    return null;
  }, [state.feed, state.lastBid, players]);

  const leaderboardAgents = useMemo(
    () =>
      [...players]
        .sort((a, b) => (state.scores[b.id] ?? 0) - (state.scores[a.id] ?? 0))
        .map((p) => ({ id: p.id, name: p.name, score: state.scores[p.id] ?? 0 })),
    [players, state.scores],
  );

  const leaderId = leaderboardAgents[0]?.id;

  return (
    <div className="flex min-h-screen flex-col">
      <TopNav />
      <div className="grid min-h-0 flex-1 grid-cols-1 lg:grid-cols-[212px_minmax(0,1fr)_340px]">
        <GoofSidebar
          round={state.round}
          totalRounds={goofMatchMeta.rounds}
          pot={state.pot}
          carryover={state.carryover}
          agents={players.length}
          elapsed={fmtElapsedG(elapsed)}
          status={feed.status}
          view={view}
          onSelect={setView}
        />

        <main className="flex min-h-0 flex-col border-x border-border-soft">
          <div className="flex items-center justify-between border-b border-border-soft px-5 py-3.5">
            <div className="flex items-center gap-3">
              <h2 className="font-display text-sm font-semibold uppercase tracking-[0.12em] text-ink-primary">{GVIEW_TITLE[view]}</h2>
              <span className="inline-flex items-center gap-1.5 font-mono text-[11px] text-emerald-400">
                <span className="live-dot h-1.5 w-1.5 rounded-full bg-emerald-400" /> {phase.label}
              </span>
            </div>
            <ConnPill status={feed.status} />
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto p-4 md:p-5">
            {view === "table" && (
              <div className={cx("goof-stage relative overflow-hidden rounded-2xl border border-border-soft p-5", isPot && "is-pot")}>
                <div className="goof-grid pointer-events-none" aria-hidden="true" />
                <div className="goof-halo pointer-events-none" aria-hidden="true" />
                <GoofDuelStage
                  players={players}
                  scores={state.scores}
                  hands={state.hands}
                  leader={leader}
                  lastBids={state.lastBid}
                  reveal={state.phase !== "reveal"}
                  prize={state.prize}
                  pot={state.pot}
                  carryover={state.carryover}
                  active={state.phase !== "final"}
                  subtitle={state.lastTie && state.phase === "showdown" ? "Tie — prize carries to next round" : undefined}
                />
                {lastThought && state.phase !== "reveal" && (
                  <ThoughtSummary agent={lastThought.agent} card={lastThought.card} text={lastThought.text} />
                )}
                <div className="mt-4">
                  <Showdown state={state} players={players} />
                </div>
                {gameOver && <VictoryBanner winner={state.winner!} scores={state.scores} players={players} />}
              </div>
            )}

            {view === "agents" && (
              <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
                {players.map((p) => (
                  <AgentCard
                    key={p.id}
                    player={p}
                    score={state.scores[p.id] ?? 0}
                    hand={state.hands[p.id] ?? []}
                    bid={state.lastBid[p.id]}
                    isLeader={leaderId === p.id}
                    isWinnerOfRound={!state.lastTie && state.lastWinner === p.id}
                    tie={state.lastTie}
                    gameWinner={state.winner === p.id}
                  />
                ))}
              </div>
            )}

            {view === "log" && <RoundLog results={state.results} players={players} />}

            {view === "reasoning" && <FeedView feed={state.feed} players={players} />}

            {view === "analytics" && (
              <div className="grid gap-4 lg:grid-cols-2">
                <GoofScoreboardTable
                  players={players}
                  scores={state.scores}
                  hands={state.hands}
                  leader={leader}
                  round={state.round}
                  totalRounds={goofMatchMeta.rounds}
                />
                <AgentLeaderboard agents={leaderboardAgents} />
                <PrizeSequence revealed={state.prizesRevealed} currentRound={state.round} total={goofMatchMeta.rounds} />
                <Legend />
              </div>
            )}

            {view === "settings" && (
              <div className="max-w-2xl">
                <div className="rounded-2xl border border-border-soft bg-surface/40 p-5">
                  <SectionLabel className="mb-3 text-ink-dim">Playback</SectionLabel>
                  <p className="mb-4 font-mono text-[11px] text-ink-faint">{state.lastModerator ?? "Shuffling the prize deck…"}</p>
                  <Controls
                    mode={mode}
                    total={total}
                    index={index}
                    setIndex={setIndex}
                    playing={playing}
                    setPlaying={setPlaying}
                    speed={speed}
                    setSpeed={setSpeed}
                    following={following}
                    stepBack={stepBack}
                    stepFwd={stepFwd}
                    jumpLive={jumpLive}
                  />
                </div>
              </div>
            )}
          </div>
        </main>

        <GoofRail
          players={players}
          scores={state.scores}
          hands={state.hands}
          leaderId={leaderId}
          round={state.round}
          totalRounds={goofMatchMeta.rounds}
          pot={state.pot}
          carryover={state.carryover}
          resolved={state.results.length}
        />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- Connection pill
function ConnPill({ status }: { status: GoofFeedStatus }) {
  const map: Record<GoofFeedStatus, { label: string; tone: Tone }> = {
    connecting: { label: "CONNECTING…", tone: "amber" },
    live: { label: "LIVE ENGINE", tone: "teal" },
    fallback: { label: "REPLAY (DEMO)", tone: "blue" },
    offline: { label: "ENGINE OFFLINE", tone: "red" },
  };
  const { label, tone } = map[status];
  return (
    <Pill tone={tone} dot>
      {label}
    </Pill>
  );
}

// ---------------------------------------------------------------- Controls
function Controls({
  mode,
  total,
  index,
  setIndex,
  playing,
  setPlaying,
  speed,
  setSpeed,
  following,
  stepBack,
  stepFwd,
  jumpLive,
}: {
  mode: Mode;
  total: number;
  index: number;
  setIndex: (fn: (i: number) => number) => void;
  playing: boolean;
  setPlaying: (b: boolean) => void;
  speed: 1 | 2 | 4;
  setSpeed: (s: 1 | 2 | 4) => void;
  following: boolean;
  stepBack: () => void;
  stepFwd: () => void;
  jumpLive: () => void;
}) {
  const atEnd = index >= total - 1;

  if (mode === "connecting") {
    return (
      <span className="font-mono text-[11px] text-ink-faint">Reaching the engine…</span>
    );
  }

  if (mode === "live") {
    return (
      <div className="flex flex-wrap items-center gap-2">
        <button onClick={stepBack} className="btn-neutral px-3 py-2" aria-label="Step back">
          <ChevronLeft width={14} height={14} />
        </button>
        <button onClick={stepFwd} className="btn-neutral px-3 py-2" aria-label="Step forward" disabled={atEnd}>
          <ChevronRight width={14} height={14} />
        </button>
        <button
          onClick={jumpLive}
          className={cx("px-4 py-2", following && atEnd ? "btn-neutral" : "btn-primary")}
          disabled={following && atEnd}
        >
          {following && atEnd ? (
            <>
              <span className="live-dot h-1.5 w-1.5 rounded-full bg-primary" /> Live
            </>
          ) : (
            <>
              <Play width={14} height={14} /> Jump to live
            </>
          )}
        </button>
      </div>
    );
  }

  // Replay mode (scripted demo fallback): full transport controls.
  return (
    <div className="flex flex-wrap items-center gap-2">
      <button onClick={() => setIndex((i) => Math.max(0, i - 1))} className="btn-neutral px-3 py-2" aria-label="Step back">
        <ChevronLeft width={14} height={14} />
      </button>
      <button
        onClick={() => {
          if (atEnd) setIndex(() => 0);
          setPlaying(!playing);
        }}
        className="btn-primary px-4 py-2"
      >
        {atEnd ? <Restart width={14} height={14} /> : playing ? <Pause width={14} height={14} /> : <Play width={14} height={14} />}
        {atEnd ? "Replay" : playing ? "Pause" : "Play"}
      </button>
      <button onClick={() => setIndex((i) => Math.min(total - 1, i + 1))} className="btn-neutral px-3 py-2" aria-label="Step forward">
        <ChevronRight width={14} height={14} />
      </button>
      <div className="ml-1 flex overflow-hidden rounded-sm border border-border-strong">
        {([1, 2, 4] as const).map((s) => (
          <button
            key={s}
            onClick={() => setSpeed(s)}
            className={cx(
              "px-2.5 py-2 font-mono text-[11px] transition",
              speed === s ? "bg-primary/20 text-primary" : "text-ink-faint hover:text-ink-dim",
            )}
          >
            {s}x
          </button>
        ))}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- Agent card
function AgentCard({
  player,
  score,
  hand,
  bid,
  isLeader,
  isWinnerOfRound,
  tie,
  gameWinner,
}: {
  player: GoofPlayer;
  score: number;
  hand: number[];
  bid?: number;
  isLeader: boolean;
  isWinnerOfRound: boolean;
  tie: boolean;
  gameWinner: boolean;
}) {
  const accent = accentText(player.accent);
  return (
    <Panel
      className={cx(
        "p-4 transition",
        gameWinner
          ? "ring-1 ring-primary-container/60 shadow-glow-teal"
          : isWinnerOfRound
            ? "ring-1 ring-secondary/40"
            : "",
      )}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2.5">
          <span className={cx("flex h-9 w-9 items-center justify-center rounded-md border border-border-strong bg-bg-deep", accent)}>
            <Cpu width={18} height={18} />
          </span>
          <div className="min-w-0">
            <div className="truncate font-display text-sm font-semibold text-ink-primary">{player.name}</div>
            <div className="truncate font-mono text-[10px] text-ink-faint">
              {player.provider} · {player.model}
            </div>
          </div>
        </div>
        <div className="text-right">
          <div className={cx("font-mono text-2xl font-semibold tabular-nums", accent)}>{score}</div>
          {isLeader && !gameWinner && <div className="label-caps text-primary">LEADING</div>}
          {gameWinner && <div className="label-caps text-primary">WINNER</div>}
        </div>
      </div>

      {/* This round's bid */}
      <div className="mt-3 flex items-center justify-between rounded-md border border-border-soft bg-bg-deep/40 px-3 py-2">
        <span className="label-caps">THIS BID</span>
        {bid != null ? (
          <span
            className={cx(
              "goof-flip flex h-8 w-7 items-center justify-center rounded border font-mono text-sm font-semibold tabular-nums",
              isWinnerOfRound
                ? "border-primary-container/70 bg-primary-container/10 text-primary"
                : tie
                  ? "border-secondary/50 bg-secondary/10 text-secondary"
                  : "border-border-strong bg-surface-slate/60 text-ink-dim",
            )}
          >
            {bid}
          </span>
        ) : (
          <span className="font-mono text-[11px] text-ink-faint">deciding…</span>
        )}
      </div>

      {/* Remaining hand */}
      <div className="mt-3">
        <div className="mb-1.5 flex items-center justify-between">
          <span className="label-caps">HAND</span>
          <span className="font-mono text-[10px] text-ink-faint">{hand.length} left</span>
        </div>
        <div className="flex flex-wrap gap-1">
          {Array.from({ length: 13 }, (_, i) => i + 1).map((c) => {
            const live = hand.includes(c);
            return (
              <span
                key={c}
                className={cx(
                  "flex h-6 w-5 items-center justify-center rounded-sm border font-mono text-[10px] tabular-nums",
                  c === bid
                    ? "border-secondary/60 bg-secondary/10 text-secondary"
                    : live
                      ? "border-border-strong bg-bg-deep/60 text-ink-dim"
                      : "border-border-soft/40 text-ink-faint/30 line-through",
                )}
              >
                {c}
              </span>
            );
          })}
        </div>
      </div>
    </Panel>
  );
}

// ---------------------------------------------------------------- Showdown
function Showdown({ state, players }: { state: GoofState; players: GoofPlayer[] }) {
  const showing = state.phase !== "reveal" && Object.keys(state.lastBid).length > 0;
  return (
    <Panel glass className="p-5">
      <div className="mb-4 flex items-center justify-between">
        <SectionLabel className="text-secondary">ROUND SHOWDOWN</SectionLabel>
        {state.lastTie && showing ? (
          <Pill tone="amber" dot>
            <Equals width={12} height={12} className="mr-1 inline" /> TIE · POT CARRIES
          </Pill>
        ) : showing ? (
          <Pill tone="teal" dot>
            {byId(players, state.lastWinner)?.name ?? "—"} TAKES {state.results.at(-1)?.awarded ?? 0}
          </Pill>
        ) : (
          <Pill tone="blue" dot>
            SECRET COMMIT
          </Pill>
        )}
      </div>

      {showing ? (
        <div className="flex flex-wrap items-stretch justify-center gap-4">
          {players.map((p, i) => {
            const card = state.lastBid[p.id];
            const isWin = !state.lastTie && state.lastWinner === p.id;
            return (
              <div key={p.id} className="flex items-center gap-4">
                {i > 0 && <span className="font-display text-2xl text-ink-faint">vs</span>}
                <div className="flex flex-col items-center gap-2">
                  <span className={cx("font-mono text-[11px]", accentText(p.accent))}>{p.name}</span>
                  <div
                    className={cx(
                      "goof-flip flex h-24 w-[68px] flex-col items-center justify-center rounded-lg border bg-panel-grad font-mono text-3xl font-semibold tabular-nums transition",
                      isWin
                        ? "border-primary-container/70 text-primary shadow-glow-teal"
                        : state.lastTie
                          ? "border-secondary/50 text-secondary"
                          : "border-border-strong text-ink-dim",
                    )}
                  >
                    {card ?? "—"}
                    <span className="mt-1 label-caps text-[8px] text-ink-faint/70">BID</span>
                  </div>
                  {isWin && <span className="label-caps text-primary">WINS PRIZE</span>}
                  {state.lastTie && <span className="label-caps text-secondary">TIE</span>}
                </div>
              </div>
            );
          })}
        </div>
      ) : (
        <div className="flex flex-col items-center justify-center gap-3 py-8 text-center">
          <Brain width={26} height={26} className="text-tertiary" />
          <p className="max-w-sm font-mono text-[12px] leading-5 text-ink-faint">
            Both agents are committing a card in secret. Bids reveal simultaneously — no
            agent can react to the other.
          </p>
        </div>
      )}
    </Panel>
  );
}

// ---------------------------------------------------------------- Reasoning feed
function FeedView({ feed, players }: { feed: GoofFeedItem[]; players: GoofPlayer[] }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
  }, [feed.length]);

  return (
    <Panel className="flex min-h-[320px] flex-col p-0">
      <div className="flex items-center justify-between border-b border-border-soft px-5 py-3.5">
        <SectionLabel className="text-primary">AGENT REASONING</SectionLabel>
        <Pill tone="teal" dot>
          LIVE TRANSCRIPT
        </Pill>
      </div>
      <div ref={ref} className="flex-1 space-y-3 overflow-y-auto px-5 py-4" style={{ maxHeight: 420 }}>
        {feed.length === 0 && (
          <p className="font-mono text-[12px] text-ink-faint">Awaiting the first prize reveal…</p>
        )}
        {feed.map((item, i) =>
          item.type === "moderator" ? (
            <div key={i} className="flex justify-center">
              <div className="rounded-md border border-border-soft bg-bg-deep/60 px-3.5 py-2 text-center">
                <span className="label-caps text-tertiary">DEALER</span>
                <p className="mt-1 font-mono text-[12px] leading-5 text-ink-dim">{item.text}</p>
              </div>
            </div>
          ) : (
            <ReasoningBubble key={i} item={item} players={players} />
          ),
        )}
      </div>
    </Panel>
  );
}

function ReasoningBubble({ item, players }: { item: GoofFeedItem; players: GoofPlayer[] }) {
  const speaker = item.from != null ? byId(players, item.from) : undefined;
  const accent = speaker ? accentText(speaker.accent) : "text-ink-dim";
  return (
    <div className="rounded-lg border border-border-soft bg-surface-slate/50 p-3">
      <div className="mb-1.5 flex items-center gap-2">
        <span className={cx("flex h-6 w-6 items-center justify-center rounded border border-border-strong bg-bg-deep", accent)}>
          <Cpu width={13} height={13} />
        </span>
        <span className="font-display text-[13px] font-semibold text-ink-primary">{speaker?.name}</span>
        <span className="font-mono text-[10px] text-ink-faint">{speaker?.model}</span>
        <span className="ml-auto rounded-full border border-border-strong px-2 py-0.5 font-mono text-[9px] uppercase tracking-caps text-ink-faint">
          reasoning
        </span>
      </div>
      <p className="font-mono text-[12.5px] leading-[1.55] text-ink-dim">{item.text}</p>
    </div>
  );
}

// ---------------------------------------------------------------- Prize sequence
function PrizeSequence({
  revealed,
  currentRound,
  total,
}: {
  revealed: number[];
  currentRound: number;
  total: number;
}) {
  return (
    <Panel className="p-5">
      <div className="mb-3 flex items-center justify-between">
        <SectionLabel className="text-tertiary">
          <Layers width={13} height={13} className="mr-1 inline" /> PRIZE DECK
        </SectionLabel>
        <span className="font-mono text-[10px] text-ink-faint">
          {revealed.length}/{total} revealed
        </span>
      </div>
      <div className="grid grid-cols-7 gap-1.5">
        {Array.from({ length: total }, (_, i) => {
          const value = revealed[i];
          const isCurrent = i + 1 === currentRound;
          const seen = value != null;
          return (
            <div
              key={i}
              className={cx(
                "flex h-9 items-center justify-center rounded-sm border font-mono text-[12px] tabular-nums",
                isCurrent
                  ? "border-primary-container/70 bg-primary-container/10 text-primary shadow-glow-teal"
                  : seen
                    ? "border-border-strong bg-bg-deep/60 text-ink-dim"
                    : "border-border-soft/50 bg-bg-deep/30 text-ink-faint/40",
              )}
            >
              {seen ? value : "?"}
            </div>
          );
        })}
      </div>
      <p className="mt-3 font-mono text-[10px] leading-4 text-ink-faint">
        The deck order is hidden in advance. Agents must plan without knowing which prizes
        are still coming.
      </p>
    </Panel>
  );
}

// ---------------------------------------------------------------- Round log
function RoundLog({ results, players }: { results: GoofRoundResult[]; players: GoofPlayer[] }) {
  return (
    <Panel className="p-5">
      <SectionLabel className="mb-3 text-secondary">ROUND LOG</SectionLabel>
      {results.length === 0 ? (
        <p className="font-mono text-[11px] text-ink-faint">No rounds resolved yet.</p>
      ) : (
        <div className="space-y-1.5">
          {results.map((r) => {
            const w = r.winner != null ? byId(players, r.winner) : null;
            return (
              <div
                key={r.round}
                className="flex items-center gap-2 rounded-md border border-border-soft bg-bg-deep/40 px-2.5 py-1.5 font-mono text-[11px]"
              >
                <span className="w-6 text-ink-faint">R{r.round}</span>
                <span className="flex h-5 w-5 items-center justify-center rounded-sm border border-border-strong text-ink-dim">
                  {r.prize}
                </span>
                {r.tie ? (
                  <span className="flex items-center gap-1 text-secondary">
                    <Equals width={11} height={11} /> tie · {r.potBefore} carried
                  </span>
                ) : (
                  <span className="flex items-center gap-1 text-ink-dim">
                    <Trophy width={11} height={11} className="text-primary" />
                    <span className="text-ink-primary">{w?.name}</span>
                    <span className="text-primary">+{r.awarded}</span>
                  </span>
                )}
              </div>
            );
          })}
        </div>
      )}
    </Panel>
  );
}

// ---------------------------------------------------------------- Legend
function Legend() {
  const rows = [
    { Icon: Crosshair, text: "Highest unique bid wins the prize" },
    { Icon: Equals, text: "Tied top bid → prize carries to the pot" },
    { Icon: Brain, text: "Cards are spent once — every choice is permanent" },
    { Icon: Trophy, text: "Most points after 13 rounds wins" },
  ];
  return (
    <Panel className="p-5">
      <SectionLabel className="mb-3 text-ink-dim">HOW IT WORKS</SectionLabel>
      <div className="space-y-2.5">
        {rows.map(({ Icon, text }, i) => (
          <div key={i} className="flex items-start gap-2.5">
            <Icon width={14} height={14} className="mt-0.5 shrink-0 text-tertiary" />
            <span className="font-mono text-[11px] leading-4 text-ink-faint">{text}</span>
          </div>
        ))}
      </div>
    </Panel>
  );
}

// ---------------------------------------------------------------- Victory
function VictoryBanner({
  winner,
  scores,
  players,
}: {
  winner: number;
  scores: Record<number, number>;
  players: GoofPlayer[];
}) {
  const champ = byId(players, winner);
  const sorted = [...players].sort((a, b) => (scores[b.id] ?? 0) - (scores[a.id] ?? 0));
  return (
    <Panel glass className="mt-5 flex flex-wrap items-center justify-between gap-4 p-6 ring-1 ring-primary-container/40">
      <div className="flex items-center gap-4">
        <span className="flex h-12 w-12 items-center justify-center rounded-lg border border-primary-container/50 text-primary">
          <Trophy width={26} height={26} />
        </span>
        <div>
          <SectionLabel className="text-primary">MATCH WINNER</SectionLabel>
          <p className="mt-1 font-display text-xl font-semibold text-ink-primary">
            {champ?.name} <span className="text-ink-faint">·</span> {champ?.model}
          </p>
        </div>
      </div>
      <div className="flex items-center gap-3 font-mono">
        {sorted.map((p, i) => (
          <div key={p.id} className="flex items-center gap-2">
            {i > 0 && <span className="text-ink-faint">–</span>}
            <span className={cx("text-2xl font-semibold tabular-nums", accentText(p.accent))}>
              {scores[p.id] ?? 0}
            </span>
          </div>
        ))}
      </div>
    </Panel>
  );
}

// ── App-shell sidebar / rail (mirrors the Mafia layout) ─────────────────────

type GoofView = "table" | "agents" | "log" | "reasoning" | "analytics" | "settings";

const GVIEW_TITLE: Record<GoofView, string> = {
  table: "Strategy Table",
  agents: "Agents",
  log: "Round Log",
  reasoning: "Reasoning",
  analytics: "Analytics",
  settings: "Settings",
};

function fmtElapsedG(s: number): string {
  const m = Math.floor(s / 60);
  const sec = s % 60;
  return m > 0 ? `${m}m ${sec}s` : `${sec}s`;
}

function GRow({ k, v, big }: { k: string; v: string; big?: boolean }) {
  return (
    <div className="mb-1.5 flex items-center justify-between">
      <span className="font-sans text-[12px] text-ink-faint">{k}</span>
      <span className={cx("font-display tabular-nums text-ink-primary", big ? "text-base font-bold" : "text-[12px] font-semibold")}>{v}</span>
    </div>
  );
}

function GoofSidebar({
  round,
  totalRounds,
  pot,
  carryover,
  agents,
  elapsed,
  status,
  view,
  onSelect,
}: {
  round: number;
  totalRounds: number;
  pot: number;
  carryover: number;
  agents: number;
  elapsed: string;
  status: GoofFeedStatus;
  view: GoofView;
  onSelect: (v: GoofView) => void;
}) {
  const nav: { label: string; key: GoofView; glyph: string }[] = [
    { label: "Strategy Table", key: "table", glyph: "♠" },
    { label: "Agents", key: "agents", glyph: "⛁" },
    { label: "Round Log", key: "log", glyph: "≣" },
    { label: "Reasoning", key: "reasoning", glyph: "✦" },
    { label: "Analytics", key: "analytics", glyph: "📈" },
    { label: "Settings", key: "settings", glyph: "⚙" },
  ];
  const health = [
    { label: "Network", ok: true },
    { label: "AI Services", ok: status !== "offline" },
    { label: "Database", ok: true },
    { label: "Security", ok: true },
  ];
  const pct = totalRounds > 0 ? Math.min(100, (round / totalRounds) * 100) : 0;
  return (
    <aside className="hidden min-h-0 flex-col justify-between overflow-y-auto border-r border-border-soft bg-surface-lowest/40 lg:flex">
      <div>
        <div className="flex items-center gap-2.5 px-5 py-4">
          <span className="grid h-8 w-8 place-items-center rounded-lg bg-emerald-500/15 text-emerald-400">◆</span>
          <span className="font-display text-sm font-bold tracking-[0.06em] text-emerald-400">AGENT ARENA</span>
        </div>
        <nav className="px-3">
          {nav.map((n) => (
            <button
              key={n.key}
              onClick={() => onSelect(n.key)}
              className={cx(
                "mb-1 flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left font-sans text-[13px] transition",
                view === n.key ? "bg-emerald-500/10 text-emerald-400" : "text-ink-dim hover:bg-surface-high/40 hover:text-ink-primary",
              )}
            >
              <span className="w-4 text-center text-[12px] opacity-80">{n.glyph}</span>
              {n.label}
            </button>
          ))}
        </nav>

        <div className="mx-3 mt-5 rounded-xl border border-border-soft bg-surface/60 p-4">
          <div className="mb-3 flex items-center justify-between">
            <span className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">Match Status</span>
            <span className="inline-flex items-center gap-1 font-mono text-[10px] text-emerald-400">
              <span className="live-dot h-1.5 w-1.5 rounded-full bg-emerald-400" />Live
            </span>
          </div>
          <GRow k="Round" v={`${Math.max(1, round)} / ${totalRounds}`} big />
          <GRow k="Pot" v={`${pot}${carryover > 0 ? ` (+${carryover})` : ""}`} />
          <GRow k="Started" v={`${elapsed} ago`} />
          <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-surface-lowest">
            <div className="h-full rounded-full bg-emerald-500 transition-[width] duration-500" style={{ width: `${pct}%` }} />
          </div>
        </div>

        <div className="mx-3 mt-4 rounded-xl border border-border-soft bg-surface/60 p-4">
          <span className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">System Health</span>
          <div className="mt-3 space-y-2.5">
            {health.map((h) => (
              <div key={h.label} className="flex items-center justify-between">
                <span className="font-sans text-[12px] text-ink-dim">{h.label}</span>
                <span className={cx("font-mono text-[11px]", h.ok ? "text-emerald-400" : "text-red-400")}>{h.ok ? "Good" : "Down"}</span>
              </div>
            ))}
          </div>
        </div>
      </div>

      <a href="/dashboard" className="m-3 flex items-center gap-3 rounded-xl border border-border-soft bg-surface/60 px-3 py-3">
        <span className="grid h-8 w-8 place-items-center rounded-full border border-border-strong text-ink-dim">◉</span>
        <span className="min-w-0">
          <span className="block font-display text-[13px] font-semibold text-ink-primary">Operator</span>
          <span className="block font-mono text-[10px] text-ink-faint">@neo_control · {agents} agents</span>
        </span>
        <span className="ml-auto text-ink-faint">⌄</span>
      </a>
    </aside>
  );
}

function GSm({ k, v, tone }: { k: string; v: number | string; tone?: string }) {
  return (
    <div>
      <div className={cx("font-display text-lg font-bold tabular-nums", tone ?? "text-ink-primary")}>{v}</div>
      <div className="font-mono text-[9px] uppercase tracking-caps text-ink-faint">{k}</div>
    </div>
  );
}

function GoofRail({
  players,
  scores,
  hands,
  leaderId,
  round,
  totalRounds,
  pot,
  carryover,
  resolved,
}: {
  players: GoofPlayer[];
  scores: Record<number, number>;
  hands: Record<number, number[]>;
  leaderId?: number;
  round: number;
  totalRounds: number;
  pot: number;
  carryover: number;
  resolved: number;
}) {
  const sorted = [...players].sort((a, b) => (scores[b.id] ?? 0) - (scores[a.id] ?? 0));
  const totalScore = sorted.reduce((s, p) => s + (scores[p.id] ?? 0), 0) || 1;
  const leader = sorted.find((p) => p.id === leaderId) ?? sorted[0];
  return (
    <aside className="hidden min-h-0 flex-col overflow-y-auto border-l border-border-soft bg-surface-lowest/40 lg:flex">
      <div className="flex items-center justify-between border-b border-border-soft px-5 py-3.5">
        <span className="font-mono text-[11px] uppercase tracking-caps text-ink-faint">Match</span>
      </div>

      {leader && (
        <div className="px-5 py-4">
          <span className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">Current leader</span>
          <div className="mt-2 flex items-center gap-3">
            <span className={cx("grid h-12 w-12 place-items-center rounded-xl border border-border-strong bg-bg-deep", accentText(leader.accent))}>
              <Cpu width={22} height={22} />
            </span>
            <div>
              <div className="font-display text-lg font-bold text-ink-primary">{leader.name}</div>
              <div className="font-mono text-[11px] text-ink-faint">{leader.model}</div>
            </div>
            <span className="ml-auto font-display text-2xl font-bold tabular-nums text-emerald-400">{scores[leader.id] ?? 0}</span>
          </div>
        </div>
      )}

      <div className="border-t border-border-soft px-5 py-4">
        <span className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">Scoreboard</span>
        <div className="mt-3 space-y-3">
          {sorted.map((p, i) => {
            const sc = scores[p.id] ?? 0;
            const left = hands[p.id]?.length ?? 0;
            return (
              <div key={p.id}>
                <div className="flex items-center justify-between font-mono text-[12px]">
                  <span className="flex items-center gap-2">
                    <span className="text-ink-faint">{i + 1}</span>
                    <span className="font-display font-semibold text-ink-primary">{p.name}</span>
                  </span>
                  <span className="font-display font-bold text-ink-primary">{sc}</span>
                </div>
                <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-bg-deep">
                  <div className="h-full rounded-full bg-emerald-500" style={{ width: `${(sc / totalScore) * 100}%` }} />
                </div>
                <div className="mt-0.5 text-right font-mono text-[9px] text-ink-faint">{left} cards left</div>
              </div>
            );
          })}
        </div>
      </div>

      <div className="mt-auto border-t border-border-soft px-5 py-4">
        <span className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">Match Summary</span>
        <div className="mt-3 grid grid-cols-3 gap-3">
          <GSm k="Round" v={`${Math.max(1, round)}/${totalRounds}`} />
          <GSm k="Pot" v={pot} tone="text-secondary" />
          <GSm k="Carryover" v={carryover} tone={carryover > 0 ? "text-secondary" : undefined} />
        </div>
        <div className="mt-3 grid grid-cols-3 gap-3">
          <GSm k="Resolved" v={resolved} />
          <GSm k="Agents" v={players.length} />
          <GSm k="Avg Resp" v="0.9s" />
        </div>
        <button className="mt-4 w-full rounded-lg bg-emerald-500 py-2.5 font-mono text-[12px] font-semibold uppercase tracking-caps text-[#06210f] transition hover:bg-emerald-400">
          End Match
        </button>
      </div>
    </aside>
  );
}
