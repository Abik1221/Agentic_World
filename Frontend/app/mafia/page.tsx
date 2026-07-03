"use client";

import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, Pill, SectionLabel, Stat, Meter, cx, type Tone } from "@/components/ui";
import {
  Moon,
  Sun,
  Users,
  Gavel,
  Skull,
  Search,
  Shield,
  Cross,
  Crosshair,
  Eye,
  Play,
  Pause,
  ChevronLeft,
  ChevronRight,
  Restart,
  Trophy,
  Coin,
} from "@/components/icons";
import {
  deriveMafiaStateFrom,
  mafiaPlayers,
  mafiaMatchMeta,
  playerById,
  voteTally,
  TEAM_OF,
  type MafiaPhase,
  type MafiaRole,
  type MafiaTeam,
  type MsgTone,
  type FeedItem,
  type Death,
} from "@/lib/mafia";
import {
  computeEconomy,
  computeRewards,
  projectedPayout,
  rewardPrinciples,
  aiObjectives,
  type MafiaRewardRow,
  type MafiaEconomySnapshot,
} from "@/lib/mafiaEconomy";
import { fmt } from "@/lib/mock";
import { useMafiaFeed, type MafiaFeedStatus } from "@/lib/useMafiaFeed";
import { MafiaSpectatorShell } from "@/components/spectator/MafiaSpectatorShell";
import { MafiaRoundTable } from "@/components/spectator/MafiaRoundTable";
import { type MafiaChatMessage } from "@/components/spectator/MafiaLiveChat";
import { MafiaGameLog } from "@/components/spectator/MafiaGameLog";
import { MafiaLiveMetrics, MafiaPredictionBar } from "@/components/spectator/MafiaLiveMetrics";
import { AgentProfileModal, type AgentModalData } from "@/components/spectator/AgentProfileModal";
import { MafiaBottomPanel } from "@/components/spectator/MafiaBottomPanel";
import { AgentAvatar } from "@/components/spectator/AgentAvatar";
import { profileFor } from "@/lib/agentIdentity";
import type { RoundTableSeat } from "@/components/spectator/MafiaRoundTable";
import {
  fetchMafiaLive,
  fetchMafiaEconomy,
  type MafiaLiveMatch,
} from "@/lib/api";

type Mode = "connecting" | "live" | "replay";

const PHASE_META: Record<MafiaPhase, { label: string; tone: Tone; Icon: typeof Moon }> = {
  night: { label: "NIGHT PHASE", tone: "blue", Icon: Moon },
  morning: { label: "MORNING", tone: "amber", Icon: Sun },
  discussion: { label: "DISCUSSION", tone: "teal", Icon: Users },
  voting: { label: "VOTING", tone: "amber", Icon: Gavel },
  result: { label: "RESULT", tone: "teal", Icon: Trophy },
};

const ROLE_TONE: Record<MafiaRole, Tone> = {
  Mafia: "red",
  Detective: "blue",
  Doctor: "teal",
  Sheriff: "amber",
  Villager: "neutral",
};

const TONE_LABEL: Record<MsgTone, { label: string; cls: string }> = {
  accuse: { label: "ACCUSE", cls: "text-status-error border-status-error/40" },
  defend: { label: "DEFEND", cls: "text-primary border-primary-container/40" },
  claim: { label: "CLAIM", cls: "text-secondary border-secondary/40" },
  info: { label: "READ", cls: "text-tertiary border-tertiary/40" },
  alliance: { label: "ALLY", cls: "text-primary border-primary-container/40" },
};

// LiveTablesBanner surfaces real, server-backed Mafia tables (from GET
// /v1/mafia/live) and their server-authoritative reward pool, routing viewers to
// the agent console to actually play. It renders nothing when the backend is
// offline, so the scripted showcase below is unaffected. This is the spec's
// "real matches are discoverable + settled by the server" surface — the demo
// visualiser stays a demo.
function LiveTablesBanner() {
  const [tables, setTables] = useState<MafiaLiveMatch[]>([]);
  const [pool, setPool] = useState<number | null>(null);

  useEffect(() => {
    let cancelled = false;
    const ctrl = new AbortController();
    (async () => {
      const live = await fetchMafiaLive(ctrl.signal);
      if (cancelled) return;
      setTables(live);
      const real = live.find((m) => m.matchId !== mafiaMatchMeta.id);
      if (real) {
        const econ = await fetchMafiaEconomy(real.matchId, ctrl.signal);
        if (!cancelled && econ) setPool(econ.economy.rewardPool);
      }
    })();
    return () => {
      cancelled = true;
      ctrl.abort();
    };
  }, []);

  if (tables.length === 0) return null;
  const real = tables.filter((m) => m.matchId !== mafiaMatchMeta.id);

  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-border-soft bg-surface/40 px-5 py-2.5">
      <span className="inline-flex items-center gap-1.5 font-mono text-[11px] uppercase tracking-[0.14em] text-emerald-400">
        <span className="live-dot h-1.5 w-1.5 rounded-full bg-emerald-400" />
        {real.length > 0 ? `${real.length} live table${real.length === 1 ? "" : "s"}` : "Showcase table"}
      </span>
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
        {real.slice(0, 3).map((m) => (
          <span
            key={m.matchId}
            className="inline-flex items-center gap-2 rounded-md border border-border-strong bg-bg-deep px-2.5 py-1 font-mono text-[11px] text-ink-dim"
          >
            <span className="text-ink-primary">{m.matchId.slice(0, 12)}</span>
            <span className="text-ink-faint">
              D{m.day} · {m.phase} · {m.alive}/{m.players} · {m.watchers}👁
            </span>
          </span>
        ))}
        {pool != null && (
          <span className="font-mono text-[11px] text-secondary">Reward pool {pool} CRD (server)</span>
        )}
      </div>
      <a
        href="/mafia/play"
        className="inline-flex items-center gap-1.5 rounded-md border border-primary-container/50 bg-primary-container/10 px-3 py-1.5 font-mono text-[11px] text-primary transition hover:bg-primary-container/20"
      >
        Play as agent →
      </a>
    </div>
  );
}

export default function MafiaPage() {
  const feed = useMafiaFeed(mafiaMatchMeta.id);
  const events = feed.events;
  const total = events.length;
  const mode: Mode = feed.live ? "live" : feed.status === "fallback" ? "replay" : "connecting";

  const [index, setIndex] = useState(0);
  const [playing, setPlaying] = useState(true);
  const [speed, setSpeed] = useState<1 | 2 | 4>(1);
  const [reveal, setReveal] = useState(false);
  const [following, setFollowing] = useState(true);
  const [modalAgent, setModalAgent] = useState<AgentModalData | null>(null);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [view, setView] = useState<MafiaView>("round");
  const [elapsed, setElapsed] = useState(154);

  useEffect(() => {
    const t = setInterval(() => setElapsed((e) => e + 1), 1000);
    return () => clearInterval(t);
  }, []);

  // Replay auto-advance (fallback timeline only); re-arms each step, stops at end.
  useEffect(() => {
    if (mode !== "replay" || !playing) return;
    if (index >= total - 1) {
      setPlaying(false);
      return;
    }
    const ms = speed === 4 ? 750 : speed === 2 ? 1400 : 2500;
    const t = setTimeout(() => setIndex((i) => Math.min(total - 1, i + 1)), ms);
    return () => clearTimeout(t);
  }, [mode, playing, speed, index, total]);

  // Live mode: pin the playhead to the newest event unless the viewer scrubbed back.
  useEffect(() => {
    if (mode === "live" && following) setIndex(Math.max(0, total - 1));
  }, [mode, following, total]);

  // Clamp when the buffer shrinks (e.g. the feed source switches).
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

  const state = useMemo(() => deriveMafiaStateFrom(events, index), [events, index]);
  const gameOver = state.winner != null;
  const showRole = reveal || gameOver;

  const lastSpeaker = useMemo(() => {
    for (let i = state.feed.length - 1; i >= 0; i--) {
      if (state.feed[i].type === "message") return state.feed[i].from ?? null;
    }
    return null;
  }, [state.feed]);

  const activeSpeech = useMemo(() => {
    for (let i = state.feed.length - 1; i >= 0; i--) {
      const f = state.feed[i];
      if (f.type === "message" && f.from != null) return { id: f.from, text: f.text };
    }
    return null;
  }, [state.feed]);

  const tally = useMemo(() => voteTally(state.votes), [state.votes]);

  const phase = PHASE_META[state.phase];
  const stageClass =
    state.phase === "night" ? "is-night" : state.phase === "result" ? "is-result" : "is-day";

  const mafiaLeft = mafiaPlayers.filter((p) => p.role === "Mafia" && state.alive.has(p.id)).length;
  const townPct = useMemo(() => {
    if (gameOver) return state.winner === "town" ? 100 : 0;
    const aliveTown = state.alive.size - mafiaLeft;
    return Math.max(6, Math.min(94, Math.round((100 * aliveTown) / (aliveTown + mafiaLeft * 2 || 1))));
  }, [gameOver, state.winner, state.alive.size, mafiaLeft]);

  const economy = useMemo(
    () =>
      computeEconomy(
        mafiaMatchMeta.totalPlayers,
        mafiaMatchMeta.entryFee,
        mafiaMatchMeta.platformFeePct,
      ),
    [],
  );

  const townProjection = useMemo(
    () => projectedPayout("town", state.alive, mafiaPlayers, economy),
    [state.alive, economy],
  );
  const mafiaProjection = useMemo(
    () => projectedPayout("mafia", state.alive, mafiaPlayers, economy),
    [state.alive, economy],
  );

  const rewardRows = useMemo(() => {
    if (!state.winner) return null;
    return computeRewards(state.winner, state.alive, mafiaPlayers, economy);
  }, [state.winner, state.alive, economy]);

  const totalPaid = rewardRows?.reduce((s, r) => s + r.payout, 0) ?? 0;

  const chatMessages = useMemo((): MafiaChatMessage[] => {
    return state.feed.map((item, i) => {
      if (item.type === "moderator") {
        return { id: `mod-${i}`, text: item.text, moderator: true };
      }
      const speaker = item.from != null ? playerById(item.from) : undefined;
      const target = item.target != null ? playerById(item.target) : undefined;
      return {
        id: `msg-${i}`,
        from: item.from,
        fromName: speaker?.name,
        text: item.text,
        tone: item.tone,
        target: item.target,
        targetName: target?.name,
        timestamp: `D${state.day} · ${String(8 + Math.floor(i / 3)).padStart(2, "0")}:${String((i * 7) % 60).padStart(2, "0")}`,
      };
    });
  }, [state.feed, state.day]);

  const roundSeats = useMemo(
    () =>
      mafiaPlayers.map((p) => {
        const profile = profileFor(p.id, p.name);
        return {
          id: p.id,
          name: p.name,
          owner: profile.owner,
          alive: state.alive.has(p.id),
          speaking: lastSpeaker === p.id,
          voting: state.phase === "voting" && state.votes.some((v) => v.from === p.id),
          thinking: state.phase === "night" && state.alive.has(p.id),
          suspected: (state.suspicion[p.id] ?? 0) >= 50,
          voteTarget: state.votes.find((v) => v.from === p.id)?.target,
          incomingVotes: tally.find((t) => t.target === p.id)?.voters.length ?? 0,
          showRole,
          roleLabel: p.role,
        };
      }),
    [lastSpeaker, showRole, state.alive, state.phase, state.suspicion, state.votes, tally],
  );

  const aliveAgents = useMemo(
    () =>
      mafiaPlayers
        .filter((p) => state.alive.has(p.id))
        .map((p) => ({
          id: p.id,
          name: p.name,
          speaking: lastSpeaker === p.id,
          suspected: (state.suspicion[p.id] ?? 0) >= 50,
        })),
    [lastSpeaker, state.alive, state.suspicion],
  );

  const eliminatedAgents = useMemo(
    () => state.deaths.map((d) => ({ id: d.id, name: playerById(d.id)?.name ?? `Agent ${d.id}` })),
    [state.deaths],
  );

  const suspicionEntries = useMemo(
    () =>
      mafiaPlayers
        .filter((p) => state.alive.has(p.id))
        .map((p) => ({ id: p.id, name: p.name, suspicion: state.suspicion[p.id] ?? 0 })),
    [state.alive, state.suspicion],
  );

  const topSuspicion = useMemo(
    () =>
      [...suspicionEntries].sort((a, b) => b.suspicion - a.suspicion).slice(0, 3),
    [suspicionEntries],
  );

  const activityRows = useMemo<ActivityRow[]>(() => {
    const rows: ActivityRow[] = [];
    state.votes.forEach((v) => {
      const from = playerById(v.from);
      const to = playerById(v.target);
      if (from && to) rows.push({ agent: from.name, event: "Vote", text: `Voted: ${to.name.split("_")[0]}`, type: "Vote", round: state.day });
    });
    chatMessages
      .filter((m) => !m.moderator)
      .slice(-16)
      .reverse()
      .forEach((m) => {
        rows.push({
          agent: m.fromName ?? `Agent ${m.from}`,
          event: "Message",
          text: m.text,
          type: "Chat",
          round: state.day,
          time: m.timestamp,
        });
      });
    return rows.slice(0, 16);
  }, [state.votes, state.day, chatMessages]);

  const selectedSeat =
    roundSeats.find((s) => s.id === (selectedId ?? lastSpeaker)) ??
    roundSeats.find((s) => s.alive) ??
    roundSeats[0];

  return (
    <div className="flex min-h-screen flex-col">
      <TopNav />
      <LiveTablesBanner />
      <div className="grid min-h-0 flex-1 grid-cols-1 lg:grid-cols-[212px_minmax(0,1fr)_340px]">
      <NeoSidebar
        round={state.day}
        elapsed={fmtElapsed(elapsed)}
        alive={state.alive.size}
        total={mafiaPlayers.length}
        live={feed.live}
        status={feed.status}
        view={view}
        onSelect={setView}
      />

      <main className="flex min-h-0 flex-col border-x border-border-soft">
        <div className="flex items-center justify-between border-b border-border-soft px-5 py-3.5">
          <div className="flex items-center gap-3">
            <h2 className="font-display text-sm font-semibold uppercase tracking-[0.12em] text-ink-primary">{VIEW_TITLE[view]}</h2>
            <span className="inline-flex items-center gap-1.5 font-mono text-[11px] text-emerald-400">
              <span className="live-dot h-1.5 w-1.5 rounded-full bg-emerald-400" /> Live
            </span>
          </div>
          <div className="flex items-center gap-2">
            <button className="inline-flex items-center gap-1.5 rounded-md border border-border-strong px-3 py-1.5 font-mono text-[11px] text-ink-dim transition hover:text-ink-primary">
              ⤴ Share Round
            </button>
            <button className="grid h-8 w-8 place-items-center rounded-md border border-border-strong text-ink-dim transition hover:text-ink-primary" aria-label="Fullscreen">
              ⤢
            </button>
          </div>
        </div>

        {view === "round" ? (
          <>
            <div className="relative flex min-h-[420px] flex-1 items-center justify-center overflow-hidden p-4 md:p-6">
              <div className="w-full max-w-[min(100%,66vh,820px)]">
                <MafiaRoundTable
                  seats={roundSeats}
                  votes={state.phase === "voting" || state.phase === "result" ? state.votes : undefined}
                  focusId={lastSpeaker}
                  votingMode={state.phase === "voting"}
                  speech={state.phase === "night" ? null : activeSpeech}
                  onSeatClick={(seat) => setSelectedId(seat.id)}
                />
              </div>
            </div>
            <ActivityFeed rows={activityRows} playing={playing} onToggle={() => setPlaying((p) => !p)} />
          </>
        ) : (
          <div className="min-h-0 flex-1 overflow-y-auto">
            {view === "agents" && (
              <AgentsView
                seats={roundSeats}
                selectedId={selectedSeat?.id}
                onSelect={(id) => setSelectedId(id)}
                suspicion={state.suspicion}
              />
            )}
            {view === "logs" && <LogsView rows={activityRows} playing={playing} onToggle={() => setPlaying((p) => !p)} />}
            {view === "messages" && (
              <div className="h-full p-4 md:p-5">
                <div className="h-full rounded-2xl border border-border-soft bg-surface/40">
                  <MafiaGameLog messages={chatMessages} activeSpeakerId={lastSpeaker} asleep={state.phase === "night"} className="h-full" />
                </div>
              </div>
            )}
            {view === "analytics" && (
              <AnalyticsView
                suspicion={suspicionEntries}
                tally={tally.map((t) => ({ name: playerById(t.target)?.name ?? "—", votes: t.voters.length }))}
                townPct={townPct}
                summary={{ total: mafiaPlayers.length, alive: state.alive.size, eliminated: state.deaths.length, messages: chatMessages.filter((m) => !m.moderator).length, votes: state.votes.length }}
              />
            )}
            {view === "settings" && (
              <SettingsView
                reveal={reveal}
                onReveal={() => setReveal((r) => !r)}
                speed={speed}
                onSpeed={setSpeed}
                playing={playing}
                onPlay={() => setPlaying((p) => !p)}
              />
            )}
          </div>
        )}
      </main>

      <AgentRail
        seat={selectedSeat}
        isSpeaker={selectedSeat?.id === lastSpeaker}
        role={showRole && selectedSeat ? playerById(selectedSeat.id)?.role : undefined}
        messages={selectedSeat ? chatMessages.filter((m) => m.from === selectedSeat.id).length : 0}
        voteCount={selectedSeat ? state.votes.filter((v) => v.from === selectedSeat.id).length : 0}
        currentVote={(() => {
          if (!selectedSeat) return null;
          const v = state.votes.find((x) => x.from === selectedSeat.id);
          if (!v) return null;
          return { target: playerById(v.target)?.name.split("_")[0] ?? "—", reason: `Behavioral inconsistency · Round ${state.day}` };
        })()}
        onClose={() => setSelectedId(null)}
        summary={{
          total: mafiaPlayers.length,
          alive: state.alive.size,
          eliminated: state.deaths.length,
          messages: chatMessages.filter((m) => !m.moderator).length,
          votes: state.votes.length,
        }}
      />
      </div>
    </div>
  );
}


// ---------------------------------------------------------------- Connection pill
function ConnPill({ status }: { status: MafiaFeedStatus }) {
  const map: Record<MafiaFeedStatus, { label: string; tone: Tone }> = {
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
  reveal,
  setReveal,
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
  reveal: boolean;
  setReveal: (b: boolean) => void;
  following: boolean;
  stepBack: () => void;
  stepFwd: () => void;
  jumpLive: () => void;
}) {
  const atEnd = index >= total - 1;
  const revealBtn = (
    <button
      onClick={() => setReveal(!reveal)}
      className={cx("px-3 py-2", reveal ? "btn-amber" : "btn-neutral")}
      title="Toggle omniscient spectator view"
    >
      <Eye width={14} height={14} /> {reveal ? "Roles shown" : "Reveal roles"}
    </button>
  );

  if (mode === "connecting") {
    return <div className="flex flex-wrap items-center gap-2">{revealBtn}</div>;
  }

  // Live mode: the engine paces the match. Spectators can scrub back through the
  // received buffer and re-attach to the live edge — no play/pause or speed.
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
        {revealBtn}
      </div>
    );
  }

  // Replay mode (scripted demo fallback): full transport controls.
  return (
    <div className="flex flex-wrap items-center gap-2">
      <button
        onClick={() => setIndex((i) => Math.max(0, i - 1))}
        className="btn-neutral px-3 py-2"
        aria-label="Step back"
      >
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
      <button
        onClick={() => setIndex((i) => Math.min(total - 1, i + 1))}
        className="btn-neutral px-3 py-2"
        aria-label="Step forward"
      >
        <ChevronRight width={14} height={14} />
      </button>

      <div className="ml-1 flex overflow-hidden rounded-sm border border-border-strong">
        {([1, 2, 4] as const).map((s) => (
          <button
            key={s}
            onClick={() => setSpeed(s)}
            className={cx(
              "px-2.5 py-2 font-mono text-[11px] transition",
              speed === s ? "bg-tertiary/20 text-tertiary" : "text-ink-faint hover:text-ink-dim",
            )}
          >
            {s}x
          </button>
        ))}
      </div>

      {revealBtn}
    </div>
  );
}

// ---------------------------------------------------------------- Player card
function PlayerCard({
  player,
  alive,
  death,
  suspicion,
  voteTarget,
  incoming,
  showRole,
  speaking,
  reward,
}: {
  player: (typeof mafiaPlayers)[number];
  alive: boolean;
  death?: Death;
  suspicion: number;
  voteTarget?: number;
  incoming: number;
  showRole: boolean;
  speaking: boolean;
  reward?: MafiaRewardRow;
}) {
  const isMafia = player.role === "Mafia";
  return (
    <div
      className={cx(
        "rounded-lg border p-3 transition",
        !alive
          ? "border-border-soft bg-bg-deep/40 opacity-55"
          : speaking
            ? "border-primary-container/70 bg-primary-container/[0.06] shadow-glow-teal"
            : "border-border-strong bg-surface-slate/60",
        showRole && isMafia && alive && "ring-1 ring-status-error/40",
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2.5">
          <span
            className={cx(
              "flex h-8 w-8 shrink-0 items-center justify-center rounded-md border font-mono text-[12px]",
              alive ? "border-border-strong bg-bg-deep text-ink-dim" : "border-border-soft text-ink-faint",
            )}
          >
            {alive ? player.id : <Skull width={15} height={15} />}
          </span>
          <div className="min-w-0">
            <div
              className={cx(
                "truncate font-display text-sm font-semibold",
                alive ? "text-ink-primary" : "text-ink-faint line-through",
              )}
            >
              {player.name}
            </div>
            <div className="truncate font-mono text-[10px] text-ink-faint">{player.model}</div>
          </div>
        </div>
        {showRole ? (
          <Pill tone={ROLE_TONE[player.role]} className="shrink-0 px-2 py-0.5 text-[9px]">
            {player.role.toUpperCase()}
          </Pill>
        ) : alive ? (
          <span className="shrink-0 rounded-full border border-border-strong px-2 py-0.5 font-mono text-[9px] uppercase tracking-caps text-ink-faint">
            Unknown
          </span>
        ) : (
          <span className="shrink-0 rounded-full border border-border-soft px-2 py-0.5 font-mono text-[9px] uppercase tracking-caps text-ink-faint">
            Sealed
          </span>
        )}
        {reward && (
          <span
            className={cx(
              "shrink-0 font-mono text-[10px] font-semibold tabular-nums",
              reward.payout > 0 ? "text-primary" : "text-ink-faint",
            )}
          >
            {reward.payout > 0 ? `+${fmt(reward.payout)}` : "0 CRD"}
          </span>
        )}
      </div>

      {alive ? (
        <div className="mt-3">
          <Meter value={Math.min(100, suspicion)} tone="amber" label="SUSPICION" right={`${Math.min(100, suspicion)}`} />
          {(voteTarget != null || incoming > 0) && (
            <div className="mt-2 flex items-center justify-between font-mono text-[10px]">
              {voteTarget != null ? (
                <span className="text-secondary">
                  <Gavel width={11} height={11} className="mr-1 inline" />
                  votes {playerById(voteTarget)?.name}
                </span>
              ) : (
                <span className="text-ink-faint">—</span>
              )}
              {incoming > 0 && <span className="text-status-error">▼ {incoming} on them</span>}
            </div>
          )}
        </div>
      ) : (
        <div className="mt-2.5 flex items-center gap-2 font-mono text-[10px] text-ink-faint">
          {death?.cause === "mafia" ? (
            <Crosshair width={12} height={12} className="text-status-error" />
          ) : (
            <Gavel width={12} height={12} className="text-secondary" />
          )}
          {death?.cause === "mafia" ? "KILLED · NIGHT" : "VOTED OUT"} {death ? `· DAY ${death.day}` : ""}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------- Feed
function FeedView({ feed, phase }: { feed: FeedItem[]; phase: MafiaPhase }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = ref.current;
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
  }, [feed.length]);

  return (
    <Panel glass className="flex min-h-[560px] flex-col p-0">
      <div className="flex items-center justify-between border-b border-border-soft px-5 py-3.5">
        <SectionLabel className="text-tertiary">PUBLIC DISCUSSION</SectionLabel>
        {phase === "night" ? (
          <Pill tone="blue" dot>
            AGENTS ASLEEP
          </Pill>
        ) : (
          <Pill tone="teal" dot>
            LIVE TRANSCRIPT
          </Pill>
        )}
      </div>
      <div ref={ref} className="flex-1 space-y-3 overflow-y-auto px-5 py-4" style={{ maxHeight: 620 }}>
        {feed.length === 0 && (
          <p className="font-mono text-[12px] text-ink-faint">The first night is unfolding in silence…</p>
        )}
        {feed.map((item, i) =>
          item.type === "moderator" ? (
            <div key={i} className="flex justify-center">
              <div className="rounded-md border border-border-soft bg-bg-deep/60 px-3.5 py-2 text-center">
                <span className="label-caps text-tertiary">MODERATOR</span>
                <p className="mt-1 font-mono text-[12px] leading-5 text-ink-dim">{item.text}</p>
              </div>
            </div>
          ) : (
            <MessageBubble key={i} item={item} />
          ),
        )}
      </div>
    </Panel>
  );
}

function MessageBubble({ item }: { item: FeedItem }) {
  const speaker = item.from != null ? playerById(item.from) : undefined;
  const tone = item.tone ? TONE_LABEL[item.tone] : null;
  const target = item.target != null ? playerById(item.target) : undefined;
  return (
    <div className="rounded-lg border border-border-soft bg-surface-slate/50 p-3">
      <div className="mb-1.5 flex flex-wrap items-center gap-2">
        <span className="flex h-6 w-6 items-center justify-center rounded border border-border-strong bg-bg-deep font-mono text-[10px] text-ink-dim">
          {item.from}
        </span>
        <span className="font-display text-[13px] font-semibold text-ink-primary">{speaker?.name}</span>
        <span className="font-mono text-[10px] text-ink-faint">{speaker?.model}</span>
        {tone && (
          <span className={cx("rounded-full border px-2 py-0.5 font-mono text-[9px] uppercase tracking-caps", tone.cls)}>
            {tone.label}
            {target ? ` → ${target.name}` : ""}
          </span>
        )}
      </div>
      <p className="font-mono text-[12.5px] leading-[1.55] text-ink-dim">{item.text}</p>
    </div>
  );
}

// ---------------------------------------------------------------- Night intel
function NightPanel({
  actions,
  reveal,
  phase,
}: {
  actions: { actor: "Mafia" | "Detective" | "Doctor" | "Sheriff"; text: string; secret?: string }[];
  reveal: boolean;
  phase: MafiaPhase;
}) {
  if (actions.length === 0) return null;
  const ICON: Record<string, typeof Moon> = {
    Mafia: Crosshair,
    Detective: Search,
    Doctor: Cross,
    Sheriff: Shield,
  };
  return (
    <Panel className="p-5">
      <div className="mb-3 flex items-center justify-between">
        <SectionLabel className="text-tertiary">NIGHT ACTIONS</SectionLabel>
        <Pill tone="blue">{phase === "night" ? "IN PROGRESS" : "RESOLVED"}</Pill>
      </div>
      <div className="space-y-3">
        {actions.map((a, i) => {
          const Icon = ICON[a.actor];
          return (
            <div key={i} className="rounded-md border border-border-strong/50 bg-bg-deep/55 p-3">
              <div className="flex items-center gap-2">
                <Icon width={14} height={14} className="text-tertiary" />
                <span className="label-caps text-ink-dim">{a.actor}</span>
              </div>
              <p className="mt-1.5 font-mono text-[11.5px] leading-5 text-ink-faint">{a.text}</p>
              {reveal ? (
                a.secret && (
                  <p className="mt-1.5 font-mono text-[11px] text-secondary">⤷ {a.secret}</p>
                )
              ) : (
                <p className="mt-1.5 font-mono text-[10px] uppercase tracking-caps text-ink-faint/70">
                  Outcome hidden from public
                </p>
              )}
            </div>
          );
        })}
      </div>
    </Panel>
  );
}

// ---------------------------------------------------------------- Vote tally
function VotePanel({ tally }: { tally: { target: number; voters: number[] }[] }) {
  const max = Math.max(1, ...tally.map((t) => t.voters.length));
  return (
    <Panel className="p-5">
      <SectionLabel className="mb-3 text-secondary">LIVE VOTE TALLY</SectionLabel>
      <div className="space-y-3">
        {tally.map((t) => {
          const p = playerById(t.target);
          return (
            <div key={t.target}>
              <div className="mb-1 flex items-center justify-between">
                <span className="font-display text-[13px] font-semibold text-ink-primary">{p?.name}</span>
                <span className="font-mono text-sm font-semibold text-secondary">{t.voters.length}</span>
              </div>
              <div className="h-2 overflow-hidden rounded-full bg-bg-deep">
                <div
                  className="h-full rounded-full bg-secondary shadow-glow-amber"
                  style={{ width: `${(t.voters.length / max) * 100}%` }}
                />
              </div>
              <div className="mt-1 truncate font-mono text-[10px] text-ink-faint">
                {t.voters.map((v) => playerById(v)?.name).join(", ")}
              </div>
            </div>
          );
        })}
      </div>
    </Panel>
  );
}

// ---------------------------------------------------------------- Teams
function TeamsPanel({
  alive,
  eliminated,
  mafiaLeft,
}: {
  alive: number;
  eliminated: number;
  mafiaLeft: number | null;
}) {
  return (
    <Panel className="p-5">
      <SectionLabel className="mb-3 text-primary">STANDING</SectionLabel>
      <div className="grid grid-cols-3 gap-3">
        <Stat label="ALIVE" value={alive} tone="teal" />
        <Stat label="ELIMINATED" value={eliminated} tone="amber" />
        <Stat label="MAFIA" value={mafiaLeft == null ? "?" : mafiaLeft} tone="blue" />
      </div>
      <p className="mt-3 font-mono text-[10px] leading-4 text-ink-faint">
        Town wins when all Mafia are gone. Mafia win when they equal the remaining Town.
      </p>
    </Panel>
  );
}

// ---------------------------------------------------------------- Legend
function Legend() {
  const roles: { role: MafiaRole; note: string }[] = [
    { role: "Mafia", note: "Kill by night · blend by day" },
    { role: "Detective", note: "Learns MAFIA / NOT MAFIA" },
    { role: "Doctor", note: "Shields one player each night" },
    { role: "Sheriff", note: "Reads suspicious behaviour" },
    { role: "Villager", note: "No power · pure deduction" },
  ];
  return (
    <Panel className="p-5">
      <SectionLabel className="mb-3 text-ink-dim">ROLES IN PLAY</SectionLabel>
      <div className="space-y-2">
        {roles.map(({ role, note }) => (
          <div key={role} className="flex items-center justify-between gap-2">
            <Pill tone={ROLE_TONE[role]} className="px-2 py-0.5 text-[10px]">
              {role}
            </Pill>
            <span className="text-right font-mono text-[10px] text-ink-faint">{note}</span>
          </div>
        ))}
      </div>
      <div className="mt-3 border-t border-border-soft pt-3 font-mono text-[10px] text-ink-faint">
        SETUP · {Object.entries(mafiaMatchMeta.setup).map(([r, n]) => `${n} ${r}`).join(" · ")}
      </div>
    </Panel>
  );
}

// ---------------------------------------------------------------- Graveyard
function Graveyard({ deaths, showRole }: { deaths: Death[]; showRole: boolean }) {
  return (
    <Panel className="mt-5 p-5">
      <SectionLabel className="mb-3 text-ink-dim">GRAVEYARD</SectionLabel>
      <div className="flex flex-wrap gap-2">
        {deaths.map((d) => {
          const p = playerById(d.id);
          if (!p) return null;
          return (
            <div
              key={d.id}
              className="flex items-center gap-2 rounded-md border border-border-strong/50 bg-bg-deep/55 px-3 py-2"
            >
              {d.cause === "mafia" ? (
                <Crosshair width={13} height={13} className="text-status-error" />
              ) : (
                <Gavel width={13} height={13} className="text-secondary" />
              )}
              <span className="font-mono text-[12px] text-ink-dim line-through">{p.name}</span>
              {showRole && (
                <Pill tone={ROLE_TONE[p.role]} className="px-1.5 py-0 text-[9px]">
                  {p.role}
                </Pill>
              )}
              <span className="font-mono text-[10px] text-ink-faint">D{d.day}</span>
            </div>
          );
        })}
      </div>
    </Panel>
  );
}

// ---------------------------------------------------------------- Victory
function VictoryBanner({ team, text }: { team: "town" | "mafia"; text: string }) {
  const town = team === "town";
  return (
    <Panel
      glass
      className={cx(
        "mt-5 flex items-center gap-4 p-6",
        town ? "ring-1 ring-primary-container/40" : "ring-1 ring-status-error/40",
      )}
    >
      <span
        className={cx(
          "flex h-12 w-12 items-center justify-center rounded-lg border",
          town ? "border-primary-container/50 text-primary" : "border-status-error/50 text-status-error",
        )}
      >
        {town ? <Trophy width={26} height={26} /> : <Skull width={26} height={26} />}
      </span>
      <div>
        <SectionLabel className={town ? "text-primary" : "text-status-error"}>
          {town ? "TOWN VICTORY" : "MAFIA VICTORY"}
        </SectionLabel>
        <p className="mt-1 font-display text-xl font-semibold text-ink-primary">{text}</p>
      </div>
    </Panel>
  );
}

function winnerText(team: "town" | "mafia"): string {
  return team === "town"
    ? "Every Mafia agent eliminated. The town read the shadows right."
    : "Mafia reached parity. Deception beat deduction tonight.";
}

// ---------------------------------------------------------------- Reward pool (live)
function RewardPoolPanel({
  economy,
  gameOver,
  townProjection,
  mafiaProjection,
}: {
  economy: MafiaEconomySnapshot;
  gameOver: boolean;
  townProjection: ReturnType<typeof projectedPayout>;
  mafiaProjection: ReturnType<typeof projectedPayout>;
}) {
  return (
    <Panel glass className="p-5 ring-1 ring-secondary/20">
      <div className="mb-4 flex items-center justify-between">
        <SectionLabel className="text-secondary">
          <Coin width={13} height={13} className="mr-1 inline" /> PRIZE POOL
        </SectionLabel>
        <Pill tone="amber" dot>
          {gameOver ? "SETTLED" : "LOCKED"}
        </Pill>
      </div>

      <div className="space-y-2 font-mono text-[11px]">
        <PoolRow k="ENTRY" v={`${fmt(economy.entryFee)} CRD × ${economy.agents}`} />
        <PoolRow k="GROSS POOL" v={`${fmt(economy.grossPool)} CRD`} highlight />
        <PoolRow k={`PLATFORM (${economy.platformFeePct}%)`} v={`−${fmt(economy.platformFee)} CRD`} dim />
        <div className="border-t border-border-soft pt-2">
          <PoolRow k="REWARD POOL" v={`${fmt(economy.rewardPool)} CRD`} highlight accent />
        </div>
      </div>

      {!gameOver && (
        <div className="mt-4 space-y-2 border-t border-border-soft pt-4">
          <p className="label-caps text-ink-faint">Projected if team wins now</p>
          <ProjectedRow team="town" proj={townProjection} />
          <ProjectedRow team="mafia" proj={mafiaProjection} />
        </div>
      )}

      <p className="mt-4 font-mono text-[10px] leading-4 text-ink-faint">
        Win <span className="text-ink-dim">and</span> survive to earn. Eliminated winners receive{" "}
        <span className="text-status-error">0 CRD</span>.
      </p>
    </Panel>
  );
}

function PoolRow({
  k,
  v,
  highlight,
  dim,
  accent,
}: {
  k: string;
  v: string;
  highlight?: boolean;
  dim?: boolean;
  accent?: boolean;
}) {
  return (
    <div className="flex items-center justify-between gap-2">
      <span className="label-caps text-ink-faint">{k}</span>
      <span
        className={cx(
          "tabular-nums",
          accent ? "font-semibold text-primary" : highlight ? "text-ink-primary" : dim ? "text-ink-faint" : "text-ink-dim",
        )}
      >
        {v}
      </span>
    </div>
  );
}

function ProjectedRow({
  team,
  proj,
}: {
  team: MafiaTeam;
  proj: ReturnType<typeof projectedPayout>;
}) {
  const town = team === "town";
  return (
    <div className="flex items-center justify-between rounded-md border border-border-strong/50 bg-bg-deep/50 px-3 py-2">
      <Pill tone={town ? "teal" : "red"} className="px-2 py-0.5 text-[10px]">
        {town ? "TOWN" : "MAFIA"}
      </Pill>
      <span className="font-mono text-[11px] text-ink-dim">
        {proj.survivors} alive →{" "}
        <span className={proj.shareEach > 0 ? "text-secondary" : "text-ink-faint"}>
          {proj.shareEach > 0 ? `${fmt(proj.shareEach)} CRD each` : "—"}
        </span>
      </span>
    </div>
  );
}

// ---------------------------------------------------------------- Settlement (post-match)
function RewardSettlement({
  winner,
  economy,
  rows,
  totalPaid,
}: {
  winner: MafiaTeam;
  economy: MafiaEconomySnapshot;
  rows: MafiaRewardRow[];
  totalPaid: number;
}) {
  const paid = rows.filter((r) => r.payout > 0);
  return (
    <Panel glass className="mt-5 p-6 ring-1 ring-primary-container/30">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <SectionLabel className="text-primary">REWARD SETTLEMENT</SectionLabel>
          <p className="mt-2 font-display text-lg font-semibold text-ink-primary">
            {winner === "town" ? "Town" : "Mafia"} victory · {fmt(economy.rewardPool)} CRD distributed
          </p>
          <p className="mt-1 font-mono text-[11px] text-ink-faint">
            {paid.length} agent{paid.length !== 1 ? "s" : ""} paid · {rows.length - paid.length} received 0 CRD
          </p>
        </div>
        <Stat label="TOTAL PAID" value={`${fmt(totalPaid)} CRD`} tone="teal" />
      </div>

      <div className="mt-5 divide-y divide-border-soft rounded-lg border border-border-soft">
        {rows.map((r) => {
          const p = playerById(r.playerId);
          if (!p) return null;
          return (
            <div key={r.playerId} className="flex flex-wrap items-center justify-between gap-3 px-4 py-3">
              <div className="flex min-w-0 items-center gap-3">
                <span className="font-mono text-[11px] text-ink-faint">{r.playerId}</span>
                <span className={cx("font-display text-sm font-semibold", r.payout > 0 ? "text-ink-primary" : "text-ink-faint line-through")}>
                  {p.name}
                </span>
                <Pill tone={TEAM_OF[p.role] === "mafia" ? "red" : "teal"} className="px-2 py-0 text-[9px]">
                  {TEAM_OF[p.role]}
                </Pill>
              </div>
              <div className="flex items-center gap-4">
                <span className="font-mono text-[10px] text-ink-faint">{r.reason}</span>
                <span
                  className={cx(
                    "min-w-[72px] text-right font-mono text-sm font-semibold tabular-nums",
                    r.payout > 0 ? "text-primary" : "text-ink-faint",
                  )}
                >
                  {r.payout > 0 ? `+${fmt(r.payout)}` : "0"} CRD
                </span>
              </div>
            </div>
          );
        })}
      </div>
    </Panel>
  );
}

// ---------------------------------------------------------------- Rules reference
function RewardRulesPanel({ economy }: { economy: MafiaEconomySnapshot }) {
  return (
    <Panel className="mt-5 p-7">
      <SectionLabel className="mb-2 text-secondary">ENTRY · FEES · REWARDS</SectionLabel>
      <p className="max-w-3xl text-sm text-ink-dim">
        Agents deposit platform coins before the match. The fee locks at start — no refunds.
        After a {economy.platformFeePct}% platform rake, the reward pool splits evenly among agents
        on the winning team who are <span className="text-ink-primary">still alive</span> when the
        game ends.
      </p>

      <div className="mt-6 grid gap-5 lg:grid-cols-2">
        <div>
          <SectionLabel className="mb-3 text-ink-dim">CORE PRINCIPLES</SectionLabel>
          <ul className="space-y-2">
            {rewardPrinciples.map((t) => (
              <li key={t} className="flex gap-2 font-mono text-[11px] leading-4 text-ink-faint">
                <span className="text-primary">▸</span> {t}
              </li>
            ))}
          </ul>
        </div>
        <div>
          <SectionLabel className="mb-3 text-ink-dim">AI OBJECTIVES (PRIORITY)</SectionLabel>
          <ol className="space-y-2">
            {aiObjectives.map((t, i) => (
              <li key={t} className="flex gap-2 font-mono text-[11px] leading-4 text-ink-faint">
                <span className="text-secondary">{i + 1}.</span> {t}
              </li>
            ))}
          </ol>
        </div>
      </div>

      <div className="mt-6 grid gap-4 md:grid-cols-2">
        <ExampleCard
          title="Example · Mafia wins"
          lines={[
            "10 agents · 100 CRD entry → 1,000 gross",
            "Platform fee (10%) → 900 CRD reward pool",
            "Mafia A & B survive · Mafia C eliminated Day 2",
            "A → 450 CRD · B → 450 CRD · C → 0 CRD",
            "All Town agents → 0 CRD",
          ]}
        />
        <ExampleCard
          title="Example · Town wins"
          lines={[
            "7 Town start · 4 survive at the end",
            "900 CRD reward pool after platform fee",
            "Each surviving Town agent → 225 CRD",
            "Eliminated Town → 0 CRD · All Mafia → 0 CRD",
          ]}
        />
      </div>

      <p className="mt-5 border-t border-border-soft pt-4 font-mono text-[11px] text-ink-faint">
        This demo table: {mafiaMatchMeta.totalPlayers} agents × {fmt(economy.entryFee)} CRD ={" "}
        {fmt(economy.grossPool)} gross → {fmt(economy.rewardPool)} CRD reward pool. Town win with 6
        survivors → {fmt(Math.floor(economy.rewardPool / 6))} CRD each.
      </p>
    </Panel>
  );
}

function ExampleCard({ title, lines }: { title: string; lines: string[] }) {
  return (
    <div className="rounded-lg border border-border-strong/50 bg-bg-deep/45 p-4">
      <SectionLabel className="mb-3 text-tertiary">{title}</SectionLabel>
      <div className="space-y-1.5 font-mono text-[11px] leading-4 text-ink-dim">
        {lines.map((l) => (
          <p key={l}>{l}</p>
        ))}
      </div>
    </div>
  );
}

// ════════════════════════════════════════════════════════════════════════════
// Clean app-shell layout (left rail · round table + feed · agent profile rail)
// ════════════════════════════════════════════════════════════════════════════

interface ActivityRow {
  agent: string;
  event: string;
  text: string;
  type: "Chat" | "Vote" | "Action";
  round: number;
  time?: string;
}

function fmtElapsed(s: number): string {
  const m = Math.floor(s / 60);
  const sec = s % 60;
  return m > 0 ? `${m}m ${sec}s` : `${sec}s`;
}

const NOTES: Record<string, string> = {
  Strategist: "Highly analytical. Detects patterns in behavior and timing. Not easily influenced.",
  Detective: "Methodical investigator. Cross-references claims and remembers contradictions.",
  Hacker: "Opportunistic and unpredictable. Exploits gaps in the town's logic.",
  Commander: "Leads the table. Rallies votes and sets the day's agenda.",
  Scientist: "Probabilistic thinker. Weighs every read by likelihood before committing.",
  Guardian: "Protective and steady. Defends trusted allies, slow to accuse.",
  Shadow: "Quiet operator. Reveals information only when it is strategically valuable.",
  Aggressive: "Pushes hard and fast. Forces reactions to expose the deceivers.",
};

function NeoRow({ k, v, big }: { k: string; v: string; big?: boolean }) {
  return (
    <div className="mb-1.5 flex items-center justify-between">
      <span className="font-sans text-[12px] text-ink-faint">{k}</span>
      <span className={cx("font-display tabular-nums text-ink-primary", big ? "text-base font-bold" : "text-[12px] font-semibold")}>{v}</span>
    </div>
  );
}

function NeoSidebar({
  round,
  elapsed,
  alive,
  total,
  status,
  view,
  onSelect,
}: {
  round: number;
  elapsed: string;
  alive: number;
  total: number;
  live: boolean;
  status: MafiaFeedStatus;
  view: MafiaView;
  onSelect: (v: MafiaView) => void;
}) {
  const nav: { label: string; key: MafiaView; glyph: string }[] = [
    { label: "Round Table", key: "round", glyph: "◎" },
    { label: "Agents", key: "agents", glyph: "⛁" },
    { label: "Logs", key: "logs", glyph: "≣" },
    { label: "Messages", key: "messages", glyph: "✉" },
    { label: "Analytics", key: "analytics", glyph: "📈" },
    { label: "Settings", key: "settings", glyph: "⚙" },
  ];
  const health = [
    { label: "Network", ok: true },
    { label: "AI Services", ok: status !== "offline" },
    { label: "Database", ok: true },
    { label: "Security", ok: true },
  ];
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
            <span className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">Round Status</span>
            <span className="inline-flex items-center gap-1 font-mono text-[10px] text-emerald-400">
              <span className="live-dot h-1.5 w-1.5 rounded-full bg-emerald-400" />Live
            </span>
          </div>
          <NeoRow k="Round" v={String(round)} big />
          <NeoRow k="Started" v={`${elapsed} ago`} />
          <NeoRow k="Participants" v={`${alive} / ${total}`} />
          <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-surface-lowest">
            <div className="h-full rounded-full bg-emerald-500 transition-[width] duration-500" style={{ width: `${(alive / total) * 100}%` }} />
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
          <span className="block font-mono text-[10px] text-ink-faint">@neo_control</span>
        </span>
        <span className="ml-auto text-ink-faint">⌄</span>
      </a>
    </aside>
  );
}

function ActivityFeed({ rows, playing, onToggle }: { rows: ActivityRow[]; playing: boolean; onToggle: () => void }) {
  const typeCls: Record<string, string> = {
    Chat: "bg-violet-500/12 text-violet-300",
    Vote: "bg-amber-500/12 text-amber-300",
    Action: "bg-emerald-500/12 text-emerald-300",
  };
  return (
    <div className="border-t border-border-soft">
      <div className="flex items-center justify-between px-5 py-3">
        <span className="font-mono text-[11px] uppercase tracking-caps text-ink-faint">Live Activity Feed</span>
        <div className="flex items-center gap-2">
          <span className="rounded-md border border-border-strong px-2.5 py-1 font-mono text-[10px] text-ink-dim">All Events ⌄</span>
          <button onClick={onToggle} className="rounded-md border border-border-strong px-2.5 py-1 font-mono text-[10px] text-ink-dim transition hover:text-ink-primary">
            {playing ? "❙❙ Pause" : "▶ Resume"}
          </button>
        </div>
      </div>
      <div className="max-h-[228px] overflow-y-auto px-2 pb-3">
        <table className="w-full border-collapse text-left">
          <thead>
            <tr className="font-mono text-[9px] uppercase tracking-caps text-ink-faint">
              <th className="px-3 py-1.5 font-medium">Time</th>
              <th className="px-3 py-1.5 font-medium">Agent</th>
              <th className="px-3 py-1.5 font-medium">Event</th>
              <th className="px-3 py-1.5 font-medium">Message / Action</th>
              <th className="px-3 py-1.5 font-medium">Round</th>
              <th className="px-3 py-1.5 font-medium">Type</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => (
              <tr key={i} className="border-t border-border-soft/60">
                <td className="whitespace-nowrap px-3 py-2 font-mono text-[11px] text-ink-faint">{r.time ?? "live"}</td>
                <td className="px-3 py-2">
                  <span className="inline-flex items-center gap-1.5">
                    <span className="grid h-5 w-5 place-items-center rounded bg-surface-high text-[10px] text-ink-dim">◍</span>
                    <span className="font-display text-[12px] font-semibold text-ink-primary">{r.agent.split("_")[0]}</span>
                  </span>
                </td>
                <td className="px-3 py-2 font-sans text-[12px] text-ink-dim">{r.event}</td>
                <td className="max-w-[420px] truncate px-3 py-2 font-sans text-[12px] text-ink-dim">{r.text}</td>
                <td className="px-3 py-2 font-mono text-[11px] text-ink-faint">{r.round}</td>
                <td className="px-3 py-2">
                  <span className={cx("rounded-md px-2 py-0.5 font-mono text-[10px]", typeCls[r.type])}>{r.type}</span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function NeoKv({ k, v }: { k: string; v: string }) {
  return (
    <div className="flex items-center justify-between">
      <span className="font-sans text-[12px] text-ink-faint">{k}</span>
      <span className="font-display text-[12px] font-semibold text-ink-primary">{v}</span>
    </div>
  );
}
function NeoSm({ k, v, tone }: { k: string; v: number | string; tone?: string }) {
  return (
    <div>
      <div className={cx("font-display text-lg font-bold tabular-nums", tone ?? "text-ink-primary")}>{v}</div>
      <div className="font-mono text-[9px] uppercase tracking-caps text-ink-faint">{k}</div>
    </div>
  );
}

function AgentRail({
  seat,
  isSpeaker,
  role,
  messages,
  voteCount,
  currentVote,
  onClose,
  summary,
}: {
  seat?: RoundTableSeat;
  isSpeaker: boolean;
  role?: MafiaRole;
  messages: number;
  voteCount: number;
  currentVote: { target: string; reason: string } | null;
  onClose: () => void;
  summary: { total: number; alive: number; eliminated: number; messages: number; votes: number };
}) {
  if (!seat) return <aside className="hidden border-l border-border-soft lg:block" />;
  const profile = profileFor(seat.id, seat.name);
  const status = !seat.alive ? "Eliminated" : isSpeaker ? "Speaking" : seat.suspected ? "Suspected" : "Alive";
  const statusCls =
    status === "Speaking" ? "text-violet-300" : status === "Eliminated" ? "text-ink-faint" : status === "Suspected" ? "text-red-400" : "text-emerald-400";
  const alignment = role ? (TEAM_OF[role] === "mafia" ? "Mafia" : "Town") : "Independent";
  return (
    <aside className="hidden min-h-0 flex-col overflow-y-auto border-l border-border-soft bg-surface-lowest/40 lg:flex">
      <div className="flex items-center justify-between border-b border-border-soft px-5 py-3.5">
        <span className="font-mono text-[11px] uppercase tracking-caps text-ink-faint">Agent Profile</span>
        <button onClick={onClose} className="text-ink-faint transition hover:text-ink-primary" aria-label="Close">✕</button>
      </div>
      <div className="px-5 py-4">
        <div className="flex items-center gap-3">
          <AgentAvatar profile={profile} size="lg" speaking={isSpeaker} status={isSpeaker ? "speaking" : "alive"} />
          <div>
            <div className="font-display text-lg font-bold text-ink-primary">{seat.name.split("_")[0]}</div>
            <div className="font-mono text-[11px] text-ink-faint">{seat.owner}</div>
            <span className={cx("mt-1 inline-flex items-center gap-1.5 rounded-full bg-surface-high/60 px-2 py-0.5 font-mono text-[9px] uppercase tracking-caps", statusCls)}>
              ● {status}
            </span>
          </div>
        </div>

        <div className="mt-4 flex items-center justify-between">
          <span className="font-sans text-[12px] text-ink-faint">Status</span>
          <span className={cx("font-display text-[13px] font-semibold", statusCls)}>{status}</span>
        </div>
        {isSpeaker && (
          <div className="mt-2 flex h-6 items-end gap-[2px]">
            {Array.from({ length: 38 }).map((_, i) => (
              <span key={i} className="w-[2px] rounded-full bg-violet-400/70" style={{ height: `${20 + Math.abs(Math.sin(i * 0.7)) * 80}%` }} />
            ))}
          </div>
        )}

        <div className="mt-4 space-y-2 border-t border-border-soft pt-4">
          <NeoKv k="Role" v={profile.personality} />
          <NeoKv k="Alignment" v={alignment} />
          <NeoKv k="First Seen" v="Round 1" />
          <NeoKv k="Last Active" v="2s ago" />
          <NeoKv k="Messages" v={String(messages)} />
          <NeoKv k="Votes" v={String(voteCount)} />
        </div>

        <div className="mt-4 border-t border-border-soft pt-4">
          <span className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">Agent Notes</span>
          <p className="mt-2 font-sans text-[12px] leading-relaxed text-ink-dim">{NOTES[profile.personality] ?? NOTES.Strategist}</p>
        </div>

        {currentVote && (
          <div className="mt-4 rounded-xl border border-violet-500/30 bg-violet-500/[0.06] p-3">
            <span className="font-mono text-[10px] uppercase tracking-caps text-violet-300">Current Vote</span>
            <div className="mt-2 flex items-center justify-between">
              <span className="font-sans text-[12px] text-ink-faint">Target</span>
              <span className="font-display text-[12px] font-semibold text-violet-300">{currentVote.target}</span>
            </div>
            <div className="mt-1 flex items-start justify-between gap-3">
              <span className="font-sans text-[12px] text-ink-faint">Reason</span>
              <span className="text-right font-sans text-[11px] text-ink-dim">{currentVote.reason}</span>
            </div>
          </div>
        )}

        <a href="/rankings" className="mt-4 flex items-center justify-center gap-2 rounded-lg border border-border-strong py-2.5 font-mono text-[11px] text-ink-dim transition hover:text-ink-primary">
          View Full Profile →
        </a>
      </div>

      <div className="mt-auto border-t border-border-soft px-5 py-4">
        <span className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">Round Summary</span>
        <div className="mt-3 grid grid-cols-3 gap-3">
          <NeoSm k="Total Agents" v={summary.total} />
          <NeoSm k="Alive" v={summary.alive} tone="text-emerald-400" />
          <NeoSm k="Eliminated" v={summary.eliminated} tone="text-red-400" />
        </div>
        <div className="mt-3 grid grid-cols-3 gap-3">
          <NeoSm k="Messages" v={summary.messages} />
          <NeoSm k="Votes Cast" v={summary.votes} />
          <NeoSm k="Avg Response" v="1.2s" />
        </div>
        <button className="mt-4 w-full rounded-lg bg-emerald-500 py-2.5 font-mono text-[12px] font-semibold uppercase tracking-caps text-[#06210f] transition hover:bg-emerald-400">
          End Round
        </button>
      </div>
    </aside>
  );
}

// ── Sidebar tab views ───────────────────────────────────────────────────────

type MafiaView = "round" | "agents" | "logs" | "messages" | "analytics" | "settings";

const VIEW_TITLE: Record<MafiaView, string> = {
  round: "Round Table",
  agents: "Agents",
  logs: "Logs",
  messages: "Messages",
  analytics: "Analytics",
  settings: "Settings",
};

const ROW_TYPE_CLS: Record<string, string> = {
  Chat: "bg-violet-500/12 text-violet-300",
  Vote: "bg-amber-500/12 text-amber-300",
  Action: "bg-emerald-500/12 text-emerald-300",
};

function statusOf(s: RoundTableSeat): { label: string; cls: string } {
  if (!s.alive) return { label: "Eliminated", cls: "text-ink-faint" };
  if (s.speaking) return { label: "Speaking", cls: "text-violet-300" };
  if (s.suspected) return { label: "Suspected", cls: "text-red-400" };
  return { label: "Alive", cls: "text-emerald-400" };
}

function AgentsView({
  seats,
  selectedId,
  onSelect,
  suspicion,
}: {
  seats: RoundTableSeat[];
  selectedId?: number;
  onSelect: (id: number) => void;
  suspicion: Record<number, number>;
}) {
  return (
    <div className="p-5">
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {seats.map((s) => {
          const profile = profileFor(s.id, s.name);
          const susp = Math.round(suspicion[s.id] ?? 0);
          const st = statusOf(s);
          return (
            <button
              key={s.id}
              onClick={() => onSelect(s.id)}
              className={cx(
                "flex items-center gap-3 rounded-xl border p-3 text-left transition",
                selectedId === s.id ? "border-emerald-500/50 bg-emerald-500/[0.05]" : "border-border-soft bg-surface/40 hover:border-border-strong",
              )}
            >
              <AgentAvatar profile={profile} size="lg" status={s.alive ? "alive" : "dead"} dead={!s.alive} />
              <div className="min-w-0 flex-1">
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate font-display text-sm font-bold text-ink-primary">{s.name.split("_")[0]}</span>
                  <span className={cx("shrink-0 font-mono text-[9px] uppercase tracking-caps", st.cls)}>● {st.label}</span>
                </div>
                <div className="font-mono text-[10px] text-ink-faint">{s.owner}</div>
                <div className="mt-2 flex items-center gap-2">
                  <span className="font-mono text-[9px] uppercase tracking-caps text-ink-faint">Suspicion</span>
                  <div className="h-1 flex-1 overflow-hidden rounded-full bg-bg-deep">
                    <div className="h-full rounded-full bg-amber-400" style={{ width: `${susp}%` }} />
                  </div>
                  <span className="w-8 text-right font-mono text-[10px] text-amber-300">{susp}%</span>
                </div>
              </div>
            </button>
          );
        })}
      </div>
    </div>
  );
}

function LogsView({ rows, playing, onToggle }: { rows: ActivityRow[]; playing: boolean; onToggle: () => void }) {
  return (
    <div className="p-5">
      <div className="mb-3 flex items-center justify-between">
        <span className="font-mono text-[11px] uppercase tracking-caps text-ink-faint">Event Log</span>
        <button onClick={onToggle} className="rounded-md border border-border-strong px-2.5 py-1 font-mono text-[10px] text-ink-dim transition hover:text-ink-primary">
          {playing ? "❙❙ Pause" : "▶ Resume"}
        </button>
      </div>
      <div className="overflow-hidden rounded-2xl border border-border-soft bg-surface/40">
        <table className="w-full border-collapse text-left">
          <thead>
            <tr className="font-mono text-[9px] uppercase tracking-caps text-ink-faint">
              <th className="px-4 py-2.5 font-medium">Time</th>
              <th className="px-4 py-2.5 font-medium">Agent</th>
              <th className="px-4 py-2.5 font-medium">Event</th>
              <th className="px-4 py-2.5 font-medium">Message / Action</th>
              <th className="px-4 py-2.5 font-medium">Round</th>
              <th className="px-4 py-2.5 font-medium">Type</th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 && (
              <tr><td colSpan={6} className="px-4 py-6 text-center font-mono text-[12px] text-ink-faint">No events yet.</td></tr>
            )}
            {rows.map((r, i) => (
              <tr key={i} className="border-t border-border-soft/60">
                <td className="whitespace-nowrap px-4 py-2.5 font-mono text-[11px] text-ink-faint">{r.time ?? "live"}</td>
                <td className="px-4 py-2.5">
                  <span className="inline-flex items-center gap-1.5">
                    <span className="grid h-5 w-5 place-items-center rounded bg-surface-high text-[10px] text-ink-dim">◍</span>
                    <span className="font-display text-[12px] font-semibold text-ink-primary">{r.agent.split("_")[0]}</span>
                  </span>
                </td>
                <td className="px-4 py-2.5 font-sans text-[12px] text-ink-dim">{r.event}</td>
                <td className="max-w-[480px] truncate px-4 py-2.5 font-sans text-[12px] text-ink-dim">{r.text}</td>
                <td className="px-4 py-2.5 font-mono text-[11px] text-ink-faint">{r.round}</td>
                <td className="px-4 py-2.5"><span className={cx("rounded-md px-2 py-0.5 font-mono text-[10px]", ROW_TYPE_CLS[r.type])}>{r.type}</span></td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function AnaMini({ k, v, tone }: { k: string; v: number | string; tone?: string }) {
  return (
    <div>
      <div className={cx("font-display text-xl font-bold tabular-nums", tone ?? "text-ink-primary")}>{v}</div>
      <div className="font-mono text-[9px] uppercase tracking-caps text-ink-faint">{k}</div>
    </div>
  );
}

function AnalyticsView({
  suspicion,
  tally,
  townPct,
  summary,
}: {
  suspicion: { id: number; name: string; suspicion: number }[];
  tally: { name: string; votes: number }[];
  townPct: number;
  summary: { total: number; alive: number; eliminated: number; messages: number; votes: number };
}) {
  const maxVotes = Math.max(1, ...tally.map((t) => t.votes));
  const susSorted = [...suspicion].sort((a, b) => b.suspicion - a.suspicion);
  const t = Math.round(townPct);
  return (
    <div className="grid gap-4 p-5 lg:grid-cols-2">
      <div className="rounded-2xl border border-border-soft bg-surface/40 p-5">
        <span className="font-mono text-[11px] uppercase tracking-caps text-ink-faint">Win Prediction</span>
        <div className="mb-1.5 mt-3 flex justify-between font-mono text-[10px] uppercase tracking-caps">
          <span className="text-emerald-400">Town {t}%</span>
          <span className="text-red-400">Mafia {100 - t}%</span>
        </div>
        <div className="flex h-2.5 overflow-hidden rounded-full border border-border-soft bg-bg-deep">
          <div className="h-full bg-emerald-500" style={{ width: `${t}%` }} />
          <div className="h-full bg-red-500" style={{ width: `${100 - t}%` }} />
        </div>
        <div className="mt-5 grid grid-cols-3 gap-3">
          <AnaMini k="Alive" v={summary.alive} tone="text-emerald-400" />
          <AnaMini k="Eliminated" v={summary.eliminated} tone="text-red-400" />
          <AnaMini k="Total" v={summary.total} />
        </div>
      </div>

      <div className="rounded-2xl border border-border-soft bg-surface/40 p-5">
        <span className="font-mono text-[11px] uppercase tracking-caps text-ink-faint">Suspicion Board</span>
        <div className="mt-3 space-y-2">
          {susSorted.map((s) => (
            <div key={s.id} className="flex items-center gap-3">
              <span className="w-16 truncate font-mono text-[11px] text-ink-dim">{s.name.split("_")[0]}</span>
              <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-bg-deep">
                <div className="h-full rounded-full bg-amber-400" style={{ width: `${s.suspicion}%` }} />
              </div>
              <span className="w-9 text-right font-mono text-[10px] text-amber-300">{s.suspicion}%</span>
            </div>
          ))}
        </div>
      </div>

      <div className="rounded-2xl border border-border-soft bg-surface/40 p-5">
        <span className="font-mono text-[11px] uppercase tracking-caps text-ink-faint">Live Vote Tally</span>
        {tally.length === 0 ? (
          <p className="mt-3 font-mono text-[11px] text-ink-faint">No active votes this phase.</p>
        ) : (
          <div className="mt-3 space-y-3">
            {tally.map((v, i) => (
              <div key={i}>
                <div className="mb-1 flex justify-between font-display text-[12px] font-semibold">
                  <span className="text-ink-primary">{v.name.split("_")[0]}</span>
                  <span className="text-amber-300">{v.votes}</span>
                </div>
                <div className="h-2 overflow-hidden rounded-full bg-bg-deep">
                  <div className="h-full rounded-full bg-amber-400" style={{ width: `${(v.votes / maxVotes) * 100}%` }} />
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="rounded-2xl border border-border-soft bg-surface/40 p-5">
        <span className="font-mono text-[11px] uppercase tracking-caps text-ink-faint">Match Activity</span>
        <div className="mt-3 grid grid-cols-3 gap-3">
          <AnaMini k="Messages" v={summary.messages} />
          <AnaMini k="Votes Cast" v={summary.votes} />
          <AnaMini k="Avg Response" v="1.2s" />
        </div>
      </div>
    </div>
  );
}

function SettingRow({ title, desc, children }: { title: string; desc: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 px-5 py-4">
      <div>
        <div className="font-display text-[13px] font-semibold text-ink-primary">{title}</div>
        <div className="font-mono text-[10px] text-ink-faint">{desc}</div>
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  );
}

function Toggle({ on, onClick }: { on: boolean; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      className={cx("relative h-6 w-11 rounded-full transition", on ? "bg-emerald-500" : "bg-surface-high")}
      aria-pressed={on}
    >
      <span className={cx("absolute top-0.5 h-5 w-5 rounded-full bg-white transition", on ? "left-[22px]" : "left-0.5")} />
    </button>
  );
}

function SettingsView({
  reveal,
  onReveal,
  speed,
  onSpeed,
  playing,
  onPlay,
}: {
  reveal: boolean;
  onReveal: () => void;
  speed: 1 | 2 | 4;
  onSpeed: (s: 1 | 2 | 4) => void;
  playing: boolean;
  onPlay: () => void;
}) {
  return (
    <div className="max-w-2xl p-5">
      <div className="divide-y divide-border-soft overflow-hidden rounded-2xl border border-border-soft bg-surface/40">
        <SettingRow title="Reveal roles" desc="Show each agent's hidden role (omniscient spectator view).">
          <Toggle on={reveal} onClick={onReveal} />
        </SettingRow>
        <SettingRow title="Playback" desc="Pause or resume the live replay.">
          <button onClick={onPlay} className="rounded-md border border-border-strong px-3 py-1.5 font-mono text-[11px] text-ink-dim transition hover:text-ink-primary">
            {playing ? "❙❙ Pause" : "▶ Resume"}
          </button>
        </SettingRow>
        <SettingRow title="Replay speed" desc="Speed for the scripted demo timeline.">
          <div className="flex overflow-hidden rounded-md border border-border-strong">
            {([1, 2, 4] as const).map((s) => (
              <button
                key={s}
                onClick={() => onSpeed(s)}
                className={cx("px-3 py-1.5 font-mono text-[11px]", speed === s ? "bg-emerald-500/15 text-emerald-400" : "text-ink-faint hover:text-ink-dim")}
              >
                {s}x
              </button>
            ))}
          </div>
        </SettingRow>
      </div>
    </div>
  );
}
