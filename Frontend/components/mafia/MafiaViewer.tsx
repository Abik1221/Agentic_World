"use client";

import * as React from "react";
import { motion, AnimatePresence } from "framer-motion";
import {
  ChevronDown,
  Clock,
  Crown,
  Eye,
  Pause,
  Play,
  Radio,
  Share2,
  Skull,
  Users,
  Volume2,
  X,
} from "lucide-react";
import { cn } from "@/lib/cn";
import { DiscussionPanel, STATUS, type DiscussionActivity, type DiscussionMessage } from "@/components/discussion/DiscussionPanel";
import { StrategyTable } from "@/components/table/StrategyTable";
import {
  INTENT,
  OUTCOME,
  ROLE_META,
  type Agent,
  type Phase,
  type Step,
} from "@/lib/mafia-demo";
import { useMafiaLiveScript } from "@/lib/useMafiaLiveScript";
import { GameOverModal, type Standing } from "@/components/game/GameOverModal";

function mafiaEventIcon(t: string) {
  const s = t.toLowerCase();
  if (s.includes("execut") || s.includes("eliminat")) return "💀";
  if (s.includes("vot")) return "🗳";
  if (s.includes("night")) return "🌙";
  if (s.includes("conclud") || s.includes("prevail") || s.includes("win")) return "🏆";
  return "💬";
}
const MAFIA_STATUS_KEY: Record<SeatState, string> = {
  idle: "observing",
  thinking: "thinking",
  speaking: "speaking",
  voting: "voting",
  waiting: "waiting",
  dead: "dead",
  disconnected: "waiting",
  winner: "winner",
};

/* ─────────────────────────────── geometry ──────────────────────────────────
   The whole table is one SVG in a 1000×1000 viewBox so it scales perfectly on
   any screen. Every seat is an individually-addressable <g data-seat> group. */
const C = 500; // center
const R_SEAT = 358; // avatar-center ring (just outside the table edge)
const R_TABLE = 250; // table surface radius
const RA = 42; // avatar radius

function polar(i: number, n: number) {
  const angle = ((-90 + (i * 360) / n) * Math.PI) / 180;
  const ox = Math.cos(angle);
  const oy = Math.sin(angle);
  return { x: C + R_SEAT * ox, y: C + R_SEAT * oy, ox, oy };
}

/* ───────────────────────────── replay engine ───────────────────────────────
   Rebuilds the full match state from step 0..upto — deterministic and
   seekable, so the timeline can jump anywhere. Suspicion is derived from
   accusations (name mentions) and accumulated votes, mirroring what a backend
   suspicion signal would drive. */
type ChatMsg = { key: number; agent: Agent; step: Step; ts: string };
type Derived = {
  phase: Phase;
  day: number;
  speaker?: string;
  dead: string[];
  votes: Record<string, string[]>; // target -> voters
  suspicion: Record<string, number>;
  chat: ChatMsg[];
  timeline: { key: number; text: string }[];
};

function replay(upto: number, script: Step[], agents: Agent[]): Derived {
  const d: Derived = { phase: "discussion", day: 1, dead: [], votes: {}, suspicion: {}, chat: [], timeline: [] };
  let prevPhase: Phase | null = null;
  for (let i = 0; i <= upto && i < script.length; i++) {
    const s = script[i];
    if (s.phase === "voting" && prevPhase !== "voting") d.votes = {};
    d.phase = s.phase;
    d.day = s.day;
    d.speaker = s.speaker;

    if (s.text) {
      const low = s.text.toLowerCase();
      for (const a of agents) {
        if (a.id === s.speaker) continue;
        if (low.includes(a.name.toLowerCase())) {
          const bump = s.intent === "accusing" ? 2 : s.intent === "analyzing" || s.intent === "reasoning" ? 1 : 0;
          if (bump) d.suspicion[a.id] = (d.suspicion[a.id] ?? 0) + bump;
        }
      }
    }
    if (s.vote) {
      (d.votes[s.vote.to] ??= []).push(s.vote.from);
      d.suspicion[s.vote.to] = (d.suspicion[s.vote.to] ?? 0) + 3;
    }
    if (s.eliminate && !d.dead.includes(s.eliminate)) d.dead.push(s.eliminate);
    if (s.text && s.speaker) {
      const agent = agents.find((a) => a.id === s.speaker)!;
      const mm = String(9 + Math.floor(i / 6)).padStart(2, "0");
      const ss = String((i * 11) % 60).padStart(2, "0");
      d.chat.push({ key: i, agent, step: s, ts: `${mm}:${ss}` });
    }
    if (s.event) d.timeline.push({ key: i, text: s.event });
    prevPhase = s.phase;
  }
  return d;
}

const PHASE_LABEL: Record<Phase, string> = { night: "Night", discussion: "Discussion", voting: "Voting", execution: "Execution" };
const PHASE_COLOR: Record<Phase, string> = { night: "#818cf8", discussion: "#e6e6ee", voting: "#f87171", execution: "#fbbf24" };
const PHASE_ORDER: Phase[] = ["night", "discussion", "voting", "execution"];
const PHASE_SHORT = ["Night", "Debate", "Vote", "Exec"];

type SeatState = "idle" | "thinking" | "speaking" | "voting" | "waiting" | "dead" | "disconnected" | "winner";

const STATUS_META: Record<SeatState, { label: string; color: string }> = {
  idle: { label: "Idle", color: "#8d8da1" },
  thinking: { label: "Thinking", color: "#818cf8" },
  speaking: { label: "Speaking", color: "#e6e6ee" },
  voting: { label: "Voting", color: "#f59e0b" },
  waiting: { label: "Waiting", color: "#8d8da1" },
  dead: { label: "Dead", color: "#6b7280" },
  disconnected: { label: "Offline", color: "#6b7280" },
  winner: { label: "Winner", color: "#facc15" },
};

function suspicion(v: number): { label: string; color: string } {
  if (v >= 5) return { label: "Highly suspected", color: "#ef4444" };
  if (v >= 3) return { label: "Suspicious", color: "#f97316" };
  if (v >= 1) return { label: "Uncertain", color: "#eab308" };
  return { label: "Trusted", color: "#22c55e" };
}

/* ═══════════════════════════════ viewer ════════════════════════════════════ */
export function MafiaViewer() {
  // Cast + script come from the live hook: it streams a real match when one is
  // available and otherwise returns the scripted demo unchanged (the default).
  const { agents, script } = useMafiaLiveScript();

  const N = agents.length;
  const idxOf = React.useCallback((id: string) => agents.findIndex((a) => a.id === id), [agents]);
  const seats = React.useMemo(() => agents.map((_, i) => polar(i, agents.length)), [agents]);

  const [idx, setIdx] = React.useState(0);
  const [playing, setPlaying] = React.useState(true);
  const [inspect, setInspect] = React.useState<string | null>(null);
  const [seconds, setSeconds] = React.useState(0);
  const [showReveal, setShowReveal] = React.useState(false);

  const lastIdx = script.length - 1;
  const ended = idx >= lastIdx;
  const d = React.useMemo(() => replay(idx, script, agents), [idx, script, agents]);
  const alive = agents.filter((a) => !d.dead.includes(a.id));

  // Execution reveal: pause on a mid-game elimination (not the terminal step).
  const curStep = script[idx];
  const isExecReveal = !ended && curStep?.phase === "execution" && !!curStep?.eliminate;
  const advance = React.useCallback(() => setIdx((i) => Math.min(i + 1, lastIdx)), [lastIdx]);

  // Advance the broadcast; voting beats tick faster. The reveal modal owns its
  // own advance, so the normal timer stands down while it is showing.
  React.useEffect(() => {
    if (!playing || idx >= lastIdx || isExecReveal) return;
    const cur = script[idx];
    const delay = cur?.phase === "voting" ? 1700 : cur?.phase === "execution" ? 2600 : 3600;
    const t = setTimeout(() => setIdx((i) => Math.min(i + 1, lastIdx)), delay);
    return () => clearTimeout(t);
  }, [idx, playing, lastIdx, isExecReveal]);

  // Reveal roles a beat after the match concludes.
  React.useEffect(() => {
    if (!ended) {
      setShowReveal(false);
      return;
    }
    const t = setTimeout(() => setShowReveal(true), 800);
    return () => clearTimeout(t);
  }, [ended]);

  const standings = React.useMemo<Standing[]>(() => {
    // The day each eliminated agent went out, from the timeline.
    const elimDay: Record<string, number> = {};
    for (const st of script) {
      if (st.eliminate && elimDay[st.eliminate] === undefined) elimDay[st.eliminate] = st.day;
    }
    const rows = agents.map((a) => {
      const meta = ROLE_META[a.role];
      const won = meta.side === OUTCOME.winningSide;
      const out = elimDay[a.id] !== undefined;
      return {
        key: a.id,
        name: a.name,
        color: a.color,
        seat: idxOf(a.id) + 1,
        outcome: (won ? "winner" : out ? "eliminated" : "survived") as Standing["outcome"],
        detail: out ? `Eliminated · Day ${elimDay[a.id]}` : "Survived to the end",
        sub: `${meta.label} · ${a.model}`,
        _won: won,
        _out: out,
        _day: elimDay[a.id] ?? 99,
      };
    });
    // Winners first; among losers, those who survived longer rank higher.
    rows.sort(
      (x, y) =>
        Number(y._won) - Number(x._won) ||
        Number(!x._out) - Number(!y._out) ||
        y._day - x._day ||
        x.name.localeCompare(y.name),
    );
    return rows.map((r, i) => ({ ...r, rank: i + 1 }));
  }, [agents, script, idxOf]);

  // Cosmetic phase timer.
  React.useEffect(() => {
    setSeconds(0);
    if (!playing || ended) return;
    const t = setInterval(() => setSeconds((s) => s + 1), 1000);
    return () => clearInterval(t);
  }, [idx, playing, ended]);

  const statusOf = React.useCallback(
    (a: Agent): SeatState => {
      if (d.dead.includes(a.id)) return "dead";
      if (ended) return ROLE_META[a.role].side === OUTCOME.winningSide ? "winner" : "idle";
      if (d.speaker === a.id) return d.phase === "voting" ? "voting" : "speaking";
      if (d.phase === "voting") return Object.values(d.votes).flat().includes(a.id) ? "voting" : "waiting";
      if (d.phase === "discussion") return "thinking";
      if (d.phase === "night") return "waiting";
      return "idle";
    },
    [d, ended],
  );

  const replayMatch = () => {
    setShowReveal(false);
    setIdx(0);
    setPlaying(true);
  };
  const toggle = () => {
    if (ended) return replayMatch();
    setPlaying((p) => !p);
  };
  const seek = (i: number) => {
    setPlaying(false);
    setIdx(Math.max(0, Math.min(i, lastIdx)));
  };

  const totalVotes = Object.values(d.votes).reduce((s, v) => s + v.length, 0);
  const mostAccused = Object.entries(d.votes).sort((a, b) => b[1].length - a[1].length)[0];
  const avgResp = Math.round(alive.reduce((s, a) => s + a.responseMs, 0) / Math.max(1, alive.length));
  const inspected = agents.find((a) => a.id === inspect);
  const queue = speakingQueue(idx, script);
  const hasSpeaker = !!d.speaker && (d.phase === "discussion" || d.phase === "voting") && !ended;

  const messages: DiscussionMessage[] = d.chat.map((m) => ({
    id: m.key,
    agentId: m.agent.id,
    agentName: m.agent.name,
    dev: m.agent.dev,
    color: m.agent.color,
    ts: m.ts,
    text: m.step.text ?? "",
    intent: m.step.intent ? INTENT[m.step.intent] : undefined,
  }));
  const activity: DiscussionActivity[] = d.timeline.map((t) => ({ id: t.key, icon: mafiaEventIcon(t.text), text: t.text, ts: `Day ${d.day}` }));
  const discStatus = (agentId: string) => {
    const a = agents.find((x) => x.id === agentId);
    return a ? STATUS[MAFIA_STATUS_KEY[statusOf(a)]] : undefined;
  };
  const typingNames = !ended && d.phase === "discussion" && queue.next ? [agents.find((a) => a.id === queue.next)!.name] : [];

  return (
    <div className="mafia-viewer flex min-h-full flex-col gap-4 rounded-xl p-4 text-fg md:p-5">
      <Header phase={d.phase} day={d.day} seconds={seconds} alive={alive.length} dead={d.dead.length} playing={playing} ended={ended} onToggle={toggle} />

      <div className="grid flex-1 grid-cols-1 gap-4 lg:grid-cols-[1.72fr_1fr]">
        {/* ── Stage ── */}
        <Panel className="mvx-stage relative flex items-center justify-center overflow-hidden p-3 lg:h-[588px]">
          <PhaseBadge phase={d.phase} />
          <SpeakingQueue queue={queue} phase={d.phase} ended={ended} agents={agents} />

          <div className="relative mx-auto aspect-square w-full max-w-[588px]">
            {/* Pyyol walnut strategy table — agents are seated around it */}
            <StrategyTable shape="round" size={0.6} night={d.phase === "night"} topDown className="absolute inset-0" />
            <Stage
              d={d}
              agents={agents}
              seats={seats}
              idxOf={idxOf}
              n={N}
              statusOf={statusOf}
              seconds={seconds}
              hasSpeaker={hasSpeaker}
              ended={ended}
              onSelect={setInspect}
            />
          </div>
        </Panel>

        {/* ── Discussion ── */}
        <DiscussionPanel
          title="Agent Discussion"
          messages={messages}
          activity={activity}
          typingNames={typingNames}
          phaseLabel={`Day ${d.day} · ${PHASE_LABEL[d.phase]}`}
          statusOf={discStatus}
          onProfile={setInspect}
          className="lg:h-[588px]"
        />
      </div>

      {/* ── Timeline (seekable) ── */}
      <SeekTimeline idx={idx} onSeek={seek} script={script} agents={agents} />

      {/* ── Live events · Statistics ── */}
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        <Panel className="p-4">
          <PanelTitle icon={Radio}>Live Events</PanelTitle>
          <div className="mt-3 space-y-2">
            {d.phase === "voting" && mostAccused ? (
              Object.entries(d.votes)
                .sort((a, b) => b[1].length - a[1].length)
                .map(([to, froms]) => {
                  const t = agents.find((x) => x.id === to)!;
                  return (
                    <div key={to} className="flex items-center justify-between text-[12px]">
                      <span className="flex items-center gap-2 text-fg-muted">
                        <span className="h-2 w-2 rounded-full" style={{ background: t.color }} />
                        {t.name}
                      </span>
                      <span className="font-mono font-semibold text-fg">
                        {froms.length} vote{froms.length > 1 ? "s" : ""}
                      </span>
                    </div>
                  );
                })
            ) : d.timeline.length ? (
              d.timeline
                .slice(-4)
                .reverse()
                .map((t) => (
                  <div key={t.key} className="flex items-start gap-2 text-[12px] text-fg-muted">
                    <span className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-brand" />
                    {t.text}
                  </div>
                ))
            ) : (
              <p className="text-[12px] text-fg-muted">
                {PHASE_LABEL[d.phase]} phase · {alive.length} agents alive.
              </p>
            )}
          </div>
        </Panel>

        <Panel className="p-4">
          <PanelTitle icon={Eye}>Match Statistics</PanelTitle>
          <div className="mt-3 grid grid-cols-3 gap-x-4 gap-y-2.5 text-[12px]">
            <Stat k="Day" v={d.day} />
            <Stat k="Alive" v={alive.length} />
            <Stat k="Eliminated" v={d.dead.length} />
            <Stat k="Votes cast" v={totalVotes} />
            <Stat k="Most accused" v={mostAccused ? agents.find((a) => a.id === mostAccused[0])!.name : "—"} />
            <Stat k="Avg response" v={`${avgResp}ms`} />
          </div>
        </Panel>
      </div>

      {inspected && <Inspector agent={inspected} status={statusOf(inspected)} susp={d.suspicion[inspected.id] ?? 0} ended={ended} onClose={() => setInspect(null)} />}

      <AnimatePresence>
        {isExecReveal && curStep?.eliminate && (
          <ExecutionReveal
            agent={agents.find((a) => a.id === curStep.eliminate)!}
            agents={agents}
            votes={Object.entries(d.votes).flatMap(([to, froms]) => froms.map((from) => ({ from, to })))}
            day={d.day}
            dead={d.dead}
            autoContinue={playing}
            nextPhaseLabel={script[idx + 1]?.phase === "night" ? "Starting Night Phase…" : "Starting Discussion…"}
            onContinue={advance}
          />
        )}
      </AnimatePresence>

      <GameOverModal
        open={showReveal}
        onClose={() => setShowReveal(false)}
        title="Mafia · Match Results"
        banner={OUTCOME.headline}
        standings={standings}
        footer={
          <>
            <button
              onClick={() => setShowReveal(false)}
              className="rounded-sm border border-border-strong px-4 py-2 font-mono text-[12px] font-semibold uppercase tracking-caps text-ink-dim transition hover:text-ink-primary"
            >
              Close
            </button>
            <button
              onClick={replayMatch}
              className="rounded-sm bg-primary-container px-4 py-2 font-mono text-[12px] font-semibold uppercase tracking-caps text-on-primary transition hover:bg-primary"
            >
              Watch again
            </button>
          </>
        }
      />
    </div>
  );
}

/* ───────────────────────── speaking queue lookahead ───────────────────────── */
function speakingQueue(idx: number, script: Step[]): { now?: string; next?: string; waiting?: string } {
  const now = script[idx]?.speaker;
  const rest: string[] = [];
  for (let i = idx + 1; i < script.length && rest.length < 2; i++) {
    const s = script[i].speaker;
    if (s && s !== now && !rest.includes(s)) rest.push(s);
  }
  return { now, next: rest[0], waiting: rest[1] };
}

/* ══════════════════════════════ the SVG stage ══════════════════════════════ */
function Stage({
  d,
  agents,
  seats,
  idxOf,
  n,
  statusOf,
  seconds,
  hasSpeaker,
  ended,
  onSelect,
}: {
  d: Derived;
  agents: Agent[];
  seats: { x: number; y: number; ox: number; oy: number }[];
  idxOf: (id: string) => number;
  n: number;
  statusOf: (a: Agent) => SeatState;
  seconds: number;
  hasSpeaker: boolean;
  ended: boolean;
  onSelect: (id: string) => void;
}) {
  const night = d.phase === "night";
  const showVotes = d.phase === "voting" || d.phase === "execution";
  const fadingVotes = d.phase === "execution";
  const phaseIdx = PHASE_ORDER.indexOf(d.phase);

  return (
    <svg viewBox="0 0 1000 1000" className="relative z-10 h-full w-full select-none overflow-visible" role="img" aria-label="Mafia table">
      <defs>
        <radialGradient id="mvxSurface" cx="50%" cy="38%" r="72%">
          <stop offset="0%" stopColor="#1c1c26" />
          <stop offset="52%" stopColor="#111119" />
          <stop offset="100%" stopColor="#08080c" />
        </radialGradient>
        <radialGradient id="mvxVignette" cx="50%" cy="42%" r="70%">
          <stop offset="55%" stopColor="#000000" stopOpacity="0" />
          <stop offset="100%" stopColor="#000000" stopOpacity="0.55" />
        </radialGradient>
        <linearGradient id="mvxMetal" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0%" stopColor="#5c5c70" />
          <stop offset="28%" stopColor="#2a2a36" />
          <stop offset="52%" stopColor="#7a7a92" />
          <stop offset="74%" stopColor="#2a2a36" />
          <stop offset="100%" stopColor="#50505f" />
        </linearGradient>
        <radialGradient id="mvxReflect" cx="50%" cy="50%" r="50%">
          <stop offset="0%" stopColor="#ffffff" stopOpacity="0.16" />
          <stop offset="100%" stopColor="#ffffff" stopOpacity="0" />
        </radialGradient>
        <radialGradient id="mvxCenter" cx="50%" cy="50%" r="50%">
          <stop offset="0%" stopColor="#6366f1" stopOpacity="0.16" />
          <stop offset="100%" stopColor="#6366f1" stopOpacity="0" />
        </radialGradient>
        <filter id="mvxNoise">
          <feTurbulence type="fractalNoise" baseFrequency="0.9" numOctaves="2" stitchTiles="stitch" />
          <feColorMatrix type="saturate" values="0" />
        </filter>
        <filter id="mvxBlur" x="-50%" y="-50%" width="200%" height="200%">
          <feGaussianBlur stdDeviation="26" />
        </filter>
        <filter id="mvxAvatarShadow" x="-60%" y="-60%" width="220%" height="220%">
          <feDropShadow dx="0" dy="6" stdDeviation="8" floodColor="#000000" floodOpacity="0.55" />
        </filter>
      </defs>

      {/* The Pyyol walnut StrategyTable renders behind this SVG — only the
          brand spotlight sits on the wood so the table shows through. */}
      <circle cx="500" cy="500" r={R_TABLE - 26} fill="url(#mvxCenter)" className="mvx-breathe" />

      {/* speaker light beam toward center (under seats) */}
      {hasSpeaker && d.speaker && <Beam fromIdx={idxOf(d.speaker)} color={agents[idxOf(d.speaker)].color} n={n} />}

      {/* night — the walnut dims via StrategyTable; keep the moon accent */}
      {night && (
        <g className="mvx-breathe" opacity="0.9">
          <circle cx="500" cy="360" r="19" fill="#cbd5e1" />
          <circle cx="509" cy="353" r="16" fill="#241B15" />
        </g>
      )}

      {/* center HUD */}
      <CenterHUD phase={d.phase} day={d.day} seconds={seconds} phaseIdx={phaseIdx} ended={ended} night={night} />

      {/* vote lines + tokens */}
      {showVotes && (
        <g>
          {Object.entries(d.votes).flatMap(([to, froms]) =>
            froms.map((from, j) => {
              const a = idxOf(from);
              const b = idxOf(to);
              if (a < 0 || b < 0) return null;
              const { d: path, ex, ey } = votePath(a, b, j, n);
              const color = agents[a].color;
              return (
                <g key={`${from}-${to}`}>
                  {/* dark halo underneath for legibility on the walnut, then the colored line */}
                  <path d={path} fill="none" stroke="#000000" strokeWidth="5.5" strokeLinecap="round" opacity="0.35" className={fadingVotes ? "mvx-line-fade" : "mvx-line"} />
                  <path d={path} fill="none" stroke={color} strokeWidth="3.4" strokeLinecap="round" opacity="0.95" className={fadingVotes ? "mvx-line-fade" : "mvx-line"} />
                  <circle cx={ex} cy={ey} r="7.5" fill={color} className="mvx-token" style={{ filter: `drop-shadow(0 0 7px ${color})` }} />
                </g>
              );
            }),
          )}
        </g>
      )}

      {/* seats */}
      {agents.map((a, i) => (
        <Seat
          key={a.id}
          agent={a}
          seat={seats[i]}
          state={statusOf(a)}
          votes={d.votes[a.id]?.length ?? 0}
          susp={d.suspicion[a.id] ?? 0}
          dim={hasSpeaker && d.speaker !== a.id && !d.dead.includes(a.id)}
          onSelect={onSelect}
        />
      ))}
    </svg>
  );
}

function Beam({ fromIdx, color, n }: { fromIdx: number; color: string; n: number }) {
  const p = polar(fromIdx, n);
  const dx = C - p.x;
  const dy = C - p.y;
  const len = Math.hypot(dx, dy) || 1;
  const ux = dx / len;
  const uy = dy / len;
  const px = -uy; // perpendicular
  const py = ux;
  const bx = p.x + ux * RA;
  const by = p.y + uy * RA;
  const w = 46;
  return (
    <polygon
      points={`${bx + px * w},${by + py * w} ${bx - px * w},${by - py * w} ${C},${C}`}
      fill={color}
      className="mvx-beam"
    />
  );
}

function CenterHUD({ phase, day, seconds, phaseIdx, ended, night }: { phase: Phase; day: number; seconds: number; phaseIdx: number; ended: boolean; night: boolean }) {
  const color = ended ? "#facc15" : PHASE_COLOR[phase];
  return (
    <g textAnchor="middle" style={{ pointerEvents: "none" }}>
      <circle cx="500" cy="500" r="120" fill="#ffffff" fillOpacity="0.015" stroke="#ffffff" strokeOpacity="0.06" />
      <text x="500" y="454" fontSize="18" letterSpacing="4" fill="#8d8da1" style={{ fontFamily: "var(--font-mono, monospace)" }}>
        {ended ? "MATCH" : `DAY ${day}`}
      </text>
      <text x="500" y="500" fontSize="40" fontWeight={800} fill={color} style={{ transition: "fill .3s ease" }}>
        {ended ? "Complete" : PHASE_LABEL[phase]}
      </text>
      {!ended && (
        <text x="500" y="534" fontSize="21" fill="#c7c7d4" style={{ fontFamily: "var(--font-mono, monospace)" }}>
          0:{String(seconds).padStart(2, "0")}
        </text>
      )}

      {/* mini phase timeline */}
      <g>
        <line x1="410" y1="566" x2="590" y2="566" stroke="#ffffff" strokeOpacity="0.12" strokeWidth="2" />
        {PHASE_ORDER.map((p, k) => {
          const x = 410 + k * 60;
          const done = !ended && k < phaseIdx;
          const cur = !ended && k === phaseIdx;
          return (
            <g key={p}>
              <circle cx={x} cy="566" r={cur ? 6.5 : 4.5} fill={cur ? PHASE_COLOR[p] : done ? "#8d8da1" : "#2a2a37"} stroke={cur ? PHASE_COLOR[p] : "none"} strokeOpacity="0.35" strokeWidth={cur ? 6 : 0} style={{ transition: "all .3s ease" }} />
              <text x={x} y="590" fontSize="13" fill={cur ? "#e6e6ee" : "#6b6b82"} style={{ fontFamily: "var(--font-mono, monospace)" }}>
                {PHASE_SHORT[k]}
              </text>
            </g>
          );
        })}
      </g>
      {night && (
        <text x="500" y="612" fontSize="13" fill="#818cf8" style={{ fontFamily: "var(--font-mono, monospace)" }}>
          mafia moving in the dark…
        </text>
      )}
    </g>
  );
}

/* ─────────────────────────────── one seat ──────────────────────────────────── */
function Seat({
  agent,
  seat,
  state,
  votes,
  susp,
  dim,
  onSelect,
}: {
  agent: Agent;
  seat: { x: number; y: number; ox: number; oy: number };
  state: SeatState;
  votes: number;
  susp: number;
  dim: boolean;
  onSelect: (id: string) => void;
}) {
  const { x, y, oy } = seat;
  const dead = state === "dead";
  const winner = state === "winner";
  const sus = suspicion(susp);
  const initial = agent.name[0];
  const above = oy < -0.05; // top hemisphere → labels above the avatar
  const chip = STATUS_META[state];
  const chipColor = state === "speaking" ? agent.color : chip.color;

  // chair back — a barely-visible curved arc behind the seat (outward side)
  const chairR = RA + 17;
  const ca = Math.atan2(oy, seat.ox);
  const a1 = ca - 1.15;
  const a2 = ca + 1.15;
  const chairPath = `M ${x + chairR * Math.cos(a1)} ${y + chairR * Math.sin(a1)} A ${chairR} ${chairR} 0 0 1 ${x + chairR * Math.cos(a2)} ${y + chairR * Math.sin(a2)}`;

  return (
    <g
      data-seat={agent.id}
      className={cn("mvx-seat cursor-pointer", state === "speaking" && "mvx-focus", dim && "mvx-dim", dead && "mvx-die")}
      onClick={() => onSelect(agent.id)}
    >
      {/* chair */}
      <path d={chairPath} fill="none" stroke="#ffffff" strokeOpacity={dead ? 0.05 : 0.14} strokeWidth="7" strokeLinecap="round" />
      <path d={chairPath} fill="none" stroke={agent.color} strokeOpacity={winner ? 0.5 : 0.14} strokeWidth="2" strokeLinecap="round" />

      {/* status animation layer (behind avatar) */}
      {state === "thinking" && (
        <>
          <circle cx={x} cy={y} r={RA + 6} fill="none" stroke={agent.color} strokeOpacity="0.5" strokeWidth="1.5" className="mvx-brain" />
          <g className="mvx-orbit">
            {[0, 1, 2].map((k) => {
              const ang = (k * 120 * Math.PI) / 180;
              return <circle key={k} cx={x + (RA + 15) * Math.cos(ang)} cy={y + (RA + 15) * Math.sin(ang)} r="4" fill={agent.color} />;
            })}
          </g>
        </>
      )}
      {state === "speaking" && (
        <>
          <circle cx={x} cy={y} r={RA + 8} fill="none" stroke={agent.color} strokeWidth="2.5" className="mvx-ripple" />
          <circle cx={x} cy={y} r={RA + 8} fill="none" stroke={agent.color} strokeWidth="2.5" className="mvx-ripple" style={{ animationDelay: "0.6s" }} />
          <circle cx={x} cy={y} r={RA + 8} fill="none" stroke={agent.color} strokeWidth="2.5" className="mvx-ripple" style={{ animationDelay: "1.2s" }} />
        </>
      )}
      {state === "voting" && <circle cx={x} cy={y} r={RA + 8} fill="none" stroke="#f59e0b" strokeWidth="3" className="mvx-vpulse" />}
      {winner && (
        <>
          <circle cx={x} cy={y} r={RA + 8} fill="none" stroke="#facc15" strokeWidth="3" className="mvx-wglow" />
          {[-1, 0, 1].map((k) => (
            <circle key={k} cx={x + k * 14} cy={y - RA - 6} r="3" fill="#facc15" className="mvx-spark" style={{ animationDelay: `${(k + 1) * 0.35}s` }} />
          ))}
        </>
      )}

      {/* suspicion ring (smoothly recolors as the read changes) */}
      {!dead && !winner && <circle cx={x} cy={y} r={RA + 7} fill="none" stroke={sus.color} strokeOpacity="0.85" strokeWidth="3.5" style={{ transition: "stroke .45s ease" }} />}

      {/* avatar */}
      <circle cx={x} cy={y} r={RA} fill={dead ? "#141419" : "#0e0f13"} stroke={dead ? "#33333c" : agent.color} strokeWidth="2.6" filter="url(#mvxAvatarShadow)" />
      <text x={x} y={y} fontSize="34" fontWeight={800} textAnchor="middle" dominantBaseline="central" fill={dead ? "#6b7280" : agent.color}>
        {initial}
      </text>

      {/* dead skull */}
      {dead && (
        <text x={x + RA - 6} y={y - RA + 10} fontSize="20" textAnchor="middle" dominantBaseline="central">
          💀
        </text>
      )}
      {/* winner crown */}
      {winner && (
        <g className="mvx-crown">
          <path d={`M ${x - 15} ${y - RA - 12} L ${x - 8} ${y - RA - 2} L ${x} ${y - RA - 16} L ${x + 8} ${y - RA - 2} L ${x + 15} ${y - RA - 12} L ${x + 12} ${y - RA + 2} L ${x - 12} ${y - RA + 2} Z`} fill="#facc15" stroke="#b8860b" strokeWidth="1" />
        </g>
      )}

      {/* vote-count badge */}
      {votes > 0 && (
        <g className="mvx-token">
          <circle cx={x + RA - 4} cy={y + RA - 4} r="14" fill="#ef4444" stroke="#0e0f13" strokeWidth="2.5" />
          <text x={x + RA - 4} y={y + RA - 4} fontSize="17" fontWeight={800} textAnchor="middle" dominantBaseline="central" fill="#fff">
            {votes}
          </text>
        </g>
      )}

      {/* labels: name · dev · status (stacked outward from the table) */}
      <SeatLabels x={x} y={y} above={above} name={agent.name} dev={agent.dev} chipLabel={chip.label} chipColor={chipColor} />
    </g>
  );
}

function SeatLabels({ x, y, above, name, dev, chipLabel, chipColor }: { x: number; y: number; above: boolean; name: string; dev: string; chipLabel: string; chipColor: string }) {
  const dir = above ? -1 : 1;
  const nameY = above ? y - RA - 40 : y + RA + 30;
  const devY = nameY + dir * 22;
  const chipY = devY + dir * 24;
  const chipW = chipLabel.length * 8.4 + 22;
  return (
    <g textAnchor="middle" style={{ pointerEvents: "none" }}>
      <text x={x} y={nameY} fontSize="26" fontWeight={700} fill="#e6e6ee">
        {name}
      </text>
      <text x={x} y={devY} fontSize="17" fill="#8d8da1" style={{ fontFamily: "var(--font-mono, monospace)" }}>
        {dev}
      </text>
      <rect x={x - chipW / 2} y={chipY - 13} width={chipW} height="24" rx="12" fill={chipColor} fillOpacity="0.14" stroke={chipColor} strokeOpacity="0.4" />
      <text x={x} y={chipY} fontSize="15" fontWeight={600} fill={chipColor} dominantBaseline="central" style={{ fontFamily: "var(--font-mono, monospace)" }}>
        {chipLabel}
      </text>
    </g>
  );
}

function votePath(fromIdx: number, toIdx: number, j: number, n: number) {
  const p = polar(fromIdx, n);
  const q = polar(toIdx, n);
  const dx = q.x - p.x;
  const dy = q.y - p.y;
  const len = Math.hypot(dx, dy) || 1;
  const ux = dx / len;
  const uy = dy / len;
  const sx = p.x + ux * RA;
  const sy = p.y + uy * RA;
  const jitter = (j % 3) * 7 - 7;
  const ex = q.x - ux * (RA + 9) - uy * jitter;
  const ey = q.y - uy * (RA + 9) + ux * jitter;
  const mx = (sx + ex) / 2;
  const my = (sy + ey) / 2;
  const tcx = C - mx;
  const tcy = C - my;
  const tl = Math.hypot(tcx, tcy) || 1;
  const cx = mx + (tcx / tl) * 50;
  const cy = my + (tcy / tl) * 50;
  return { d: `M ${sx} ${sy} Q ${cx} ${cy} ${ex} ${ey}`, ex, ey };
}

/* ───────────────────────── speaking-queue floating card ────────────────────── */
function SpeakingQueue({ queue, phase, ended, agents }: { queue: { now?: string; next?: string; waiting?: string }; phase: Phase; ended: boolean; agents: Agent[] }) {
  if (ended || phase === "night" || !queue.now) return null;
  const row = (label: string, id: string | undefined, dim?: boolean) => {
    if (!id) return null;
    const a = agents.find((x) => x.id === id)!;
    return (
      <div className={cn("flex items-center gap-2", dim && "opacity-60")}>
        <span className="w-12 shrink-0 font-mono text-[9px] uppercase tracking-wider text-fg-muted">{label}</span>
        <span className="flex h-5 w-5 items-center justify-center rounded-full border text-[10px] font-bold" style={{ borderColor: a.color, color: a.color }}>
          {a.name[0]}
        </span>
        <span className="text-[12px] font-semibold text-fg">{a.name}</span>
      </div>
    );
  };
  return (
    <div className="absolute left-4 top-4 z-20 w-[190px] rounded-xl border border-line bg-panel/70 p-3 shadow-lg backdrop-blur">
      <div className="mb-2 flex items-center gap-1.5">
        <Volume2 className="h-3 w-3 text-brand" />
        <span className="font-mono text-[9px] uppercase tracking-wider text-fg-muted">Speaking Queue</span>
      </div>
      <div className="space-y-1.5">
        {row("Now", queue.now)}
        {row("Next", queue.next, true)}
        {row("Waiting", queue.waiting, true)}
      </div>
    </div>
  );
}

/* ═══════════════════════════ Discord-style chat ════════════════════════════ */
/* ═════════════════════════════ seek timeline ═══════════════════════════════ */
function SeekTimeline({ idx, onSeek, script, agents }: { idx: number; onSeek: (i: number) => void; script: Step[]; agents: Agent[] }) {
  const events = script.map((s, i) => ({ i, s })).filter((e) => e.s.event);
  return (
    <Panel className="p-4">
      <div className="flex items-center justify-between">
        <PanelTitle icon={Clock}>Timeline · click to replay</PanelTitle>
        <span className="font-mono text-[10px] text-fg-muted">
          {idx + 1}/{script.length}
        </span>
      </div>
      {/* segmented scrubber */}
      <div className="mt-3 flex gap-[3px]">
        {script.map((s, i) => (
          <button
            key={i}
            onClick={() => onSeek(i)}
            title={s.event ?? `${PHASE_LABEL[s.phase]}${s.speaker ? ` · ${agents.find((a) => a.id === s.speaker)?.name}` : ""}`}
            className={cn("h-2 flex-1 rounded-full transition-all", i <= idx ? "opacity-100" : "opacity-30 hover:opacity-60")}
            style={{ background: i === idx ? "#e6e6ee" : PHASE_COLOR[s.phase] }}
          />
        ))}
      </div>
      {/* event chips */}
      <div className="mt-3 flex flex-wrap gap-2">
        {events.map((e) => (
          <button
            key={e.i}
            onClick={() => onSeek(e.i)}
            className={cn(
              "flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-left text-[11px] transition",
              e.i <= idx ? "border-line bg-panel-2/50 text-fg" : "border-line/60 text-fg-muted hover:border-brand/40 hover:text-brand",
            )}
          >
            <span className="h-1.5 w-1.5 shrink-0 rounded-full" style={{ background: PHASE_COLOR[e.s.phase] }} />
            {e.s.event}
          </button>
        ))}
      </div>
    </Panel>
  );
}

/* ═════════════════════════════ execution reveal ════════════════════════════ */
function ExecutionReveal({
  agent,
  agents,
  votes,
  day,
  dead,
  autoContinue,
  nextPhaseLabel,
  onContinue,
}: {
  agent: Agent;
  agents: Agent[];
  votes: { from: string; to: string }[];
  day: number;
  dead: string[];
  autoContinue: boolean;
  nextPhaseLabel: string;
  onContinue: () => void;
}) {
  const role = ROLE_META[agent.role];
  const success = role.side === "mafia"; // the town removed a mafia → correct call
  const [stage, setStage] = React.useState(0); // 0 counting · 1 flipped · 2 result · 3 next

  const votesReceived = votes.filter((v) => v.to === agent.id).length;
  const aliveList = agents.filter((a) => !dead.includes(a.id));
  const mafiaLeft = aliveList.filter((a) => ROLE_META[a.role].side === "mafia").length;
  const civLeft = aliveList.length - mafiaLeft;

  const confetti = React.useMemo(
    () =>
      Array.from({ length: 16 }).map((_, i) => ({
        x: (Math.random() - 0.5) * 260,
        y: 60 + Math.random() * 140,
        r: Math.random() * 540 - 270,
        delay: Math.random() * 0.3,
        color: ["#facc15", "#fbbf24", "#f59e0b", "#fde68a"][i % 4],
        left: 20 + Math.random() * 60,
      })),
    [],
  );

  // cinematic stage timeline (runs once per elimination)
  React.useEffect(() => {
    const t1 = setTimeout(() => setStage(1), 800);
    const t2 = setTimeout(() => setStage(2), 1600);
    const t3 = setTimeout(() => setStage(3), 2500);
    return () => {
      clearTimeout(t1);
      clearTimeout(t2);
      clearTimeout(t3);
    };
  }, []);

  // auto-continue when playing
  React.useEffect(() => {
    if (!autoContinue) return;
    const t = setTimeout(onContinue, 3600);
    return () => clearTimeout(t);
  }, [autoContinue, onContinue]);

  const accent = success ? "#facc15" : "#ef4444";

  return (
    <motion.div className="fixed inset-0 z-50 flex items-center justify-center p-4" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} role="dialog" aria-modal="true">
      <div className="absolute inset-0 bg-black/80 backdrop-blur-md" />

      {/* confetti (success only) */}
      {success && stage >= 2 && (
        <div className="pointer-events-none absolute inset-0 overflow-hidden">
          {confetti.map((c, i) => (
            <span
              key={i}
              className="mxr-confetti absolute top-1/3 h-2 w-2 rounded-[2px]"
              style={{ left: `${c.left}%`, background: c.color, ["--mxr-x" as string]: `${c.x}px`, ["--mxr-y" as string]: `${c.y}px`, ["--mxr-r" as string]: `${c.r}deg`, animationDelay: `${c.delay}s` } as React.CSSProperties}
            />
          ))}
        </div>
      )}

      <motion.div
        className={cn("relative max-h-[92vh] w-full max-w-md overflow-y-auto rounded-2xl border border-line bg-panel/90 p-6 text-center shadow-2xl backdrop-blur", !success && stage >= 2 && "mxr-shake")}
        initial={{ scale: 0.94, y: 16 }}
        animate={{ scale: 1, y: 0 }}
        exit={{ scale: 0.96, opacity: 0 }}
        transition={{ type: "spring", stiffness: 240, damping: 26 }}
      >
        <p className="font-mono text-[11px] uppercase tracking-[0.28em] text-fg-muted">Voting Complete</p>

        {/* spotlight + enlarging avatar */}
        <div className="relative mx-auto mt-4 flex h-24 w-24 items-center justify-center">
          <span className="mxr-spot absolute inset-[-24px] rounded-full" style={{ background: `radial-gradient(circle, ${accent}44, transparent 70%)` }} />
          <motion.span
            className={cn("relative flex h-24 w-24 items-center justify-center rounded-full border-2 bg-panel text-3xl font-bold", success && stage >= 2 && "mxr-glow")}
            style={{ borderColor: agent.color, color: agent.color }}
            initial={{ scale: 0.8 }}
            animate={{ scale: 1 }}
            transition={{ duration: 0.5, ease: [0.22, 1, 0.36, 1] }}
          >
            {agent.name[0]}
          </motion.span>
        </div>

        <h2 className="mt-3 text-2xl font-semibold text-fg">{agent.name}</h2>
        <p className="font-mono text-[12px] text-fg-muted">{agent.dev}</p>

        <div className="mt-3 inline-flex items-center gap-1.5 rounded-full border border-red-500/40 bg-red-500/10 px-3 py-1 font-mono text-[11px] font-semibold uppercase tracking-[0.2em] text-red-400">
          <Skull className="h-3.5 w-3.5" /> Eliminated
        </div>

        {/* identity flip card */}
        <div className="mt-5 flex justify-center" style={{ perspective: 1000 }}>
          <motion.div className="relative h-28 w-44" style={{ transformStyle: "preserve-3d" }} animate={{ rotateY: stage >= 1 ? 180 : 0 }} transition={{ duration: 0.7, ease: [0.22, 1, 0.36, 1] }}>
            {/* front — sealed */}
            <div className="absolute inset-0 flex flex-col items-center justify-center rounded-xl border border-line bg-panel-2/60" style={{ backfaceVisibility: "hidden" }}>
              <span className="text-3xl">🂠</span>
              <span className="mt-1 font-mono text-[10px] uppercase tracking-wider text-fg-muted">Identity sealed</span>
            </div>
            {/* back — role */}
            <div className="absolute inset-0 flex flex-col items-center justify-center rounded-xl border" style={{ backfaceVisibility: "hidden", transform: "rotateY(180deg)", borderColor: `${role.color}66`, background: `${role.color}18` }}>
              <span className="text-3xl">{role.glyph}</span>
              <span className="mt-1 text-lg font-bold" style={{ color: role.color }}>
                {role.label}
              </span>
            </div>
          </motion.div>
        </div>

        {stage >= 1 && <p className="mx-auto mt-3 max-w-xs text-[12px] text-fg-muted">{role.desc}</p>}

        {/* result message */}
        {stage >= 2 && (
          <motion.div initial={{ opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.3 }} className="mt-4">
            <div className="inline-flex items-center gap-2 rounded-xl border px-4 py-2 text-[14px] font-semibold" style={{ borderColor: `${accent}55`, background: `${accent}14`, color: accent }}>
              <span>{success ? "✓" : "⚠"}</span>
              {success ? "The Town eliminated a Mafia member" : `The Town executed an innocent ${role.label}`}
            </div>
          </motion.div>
        )}

        {/* vote summary */}
        <div className="mt-5 rounded-xl border border-line bg-panel-2/40 p-3 text-left">
          <p className="font-mono text-[10px] uppercase tracking-wider text-fg-muted">Vote Results</p>
          <div className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1.5">
            {votes.map((v, i) => {
              const from = agents.find((a) => a.id === v.from)!;
              const to = agents.find((a) => a.id === v.to)!;
              const hit = v.to === agent.id;
              return (
                <motion.div key={`${v.from}-${v.to}-${i}`} initial={{ opacity: 0, x: -6 }} animate={{ opacity: 1, x: 0 }} transition={{ delay: 0.1 + i * 0.05 }} className={cn("flex items-center gap-1.5 text-[11px]", hit ? "text-fg" : "text-fg-muted")}>
                  <span className="h-2 w-2 rounded-full" style={{ background: from.color }} />
                  <span className="truncate">{from.name}</span>
                  <span style={{ color: hit ? accent : undefined }}>→</span>
                  <span className="truncate font-medium">{to.name}</span>
                </motion.div>
              );
            })}
          </div>
        </div>

        {/* statistics */}
        <div className="mt-4 grid grid-cols-3 gap-2 text-center">
          <RevealStat k="Votes" v={votesReceived} />
          <RevealStat k="Day" v={day} />
          <RevealStat k="Remaining" v={aliveList.length} />
          <RevealStat k="Mafia left" v={mafiaLeft} tone="#ef4444" />
          <RevealStat k="Civilians" v={civLeft} tone="#22c55e" />
          <RevealStat k="Eliminated" v={dead.length} />
        </div>

        {/* remaining players */}
        <div className="mt-4 flex flex-wrap items-center justify-center gap-1.5">
          {agents.map((a) => {
            const out = dead.includes(a.id);
            return (
              <span
                key={a.id}
                title={a.name}
                className={cn("relative flex h-7 w-7 items-center justify-center rounded-full border text-[10px] font-bold", out && "opacity-35")}
                style={{ borderColor: a.color, color: a.color }}
              >
                {a.name[0]}
                {out && <span className="absolute h-px w-8 rotate-45 bg-fg-muted" />}
              </span>
            );
          })}
        </div>

        {/* footer */}
        <div className="mt-5 flex items-center justify-between">
          <span className="font-mono text-[11px] text-fg-muted">{stage >= 3 ? nextPhaseLabel : "Revealing…"}</span>
          <button onClick={onContinue} className="flex items-center gap-1.5 rounded-lg border border-line bg-panel px-3 py-1.5 text-[12px] font-medium text-fg transition hover:border-brand/40 hover:text-brand">
            <Play className="h-3.5 w-3.5" /> Continue now
          </button>
        </div>
      </motion.div>
    </motion.div>
  );
}

function RevealStat({ k, v, tone }: { k: string; v: React.ReactNode; tone?: string }) {
  return (
    <div className="rounded-lg border border-line bg-panel-2/40 py-2">
      <div className="text-base font-semibold" style={{ color: tone ?? "rgb(var(--k-fg))" }}>
        {v}
      </div>
      <div className="font-mono text-[9px] uppercase tracking-wider text-fg-muted">{k}</div>
    </div>
  );
}

/* ═════════════════════════════ end-game reveal ═════════════════════════════ */
function RoleReveal({ onReplay, onClose, agents }: { onReplay: () => void; onClose: () => void; agents: Agent[] }) {
  const town = OUTCOME.winningSide === "town";
  return (
    <motion.div className="fixed inset-0 z-50 flex items-center justify-center p-4" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} role="dialog" aria-modal="true">
      <div className="absolute inset-0 bg-black/80 backdrop-blur-md" onClick={onClose} />
      <motion.div
        className="relative w-full max-w-3xl rounded-2xl border border-line bg-panel/90 p-6 shadow-2xl backdrop-blur"
        initial={{ scale: 0.94, y: 16 }}
        animate={{ scale: 1, y: 0 }}
        transition={{ type: "spring", stiffness: 240, damping: 26 }}
      >
        <button onClick={onClose} className="absolute right-4 top-4 rounded-md p-1.5 text-fg-muted hover:bg-panel-2 hover:text-fg">
          <X className="h-4 w-4" />
        </button>
        <div className="text-center">
          <div className="inline-flex items-center gap-2 rounded-full border px-3 py-1 font-mono text-[11px] uppercase tracking-wider" style={{ borderColor: town ? "#22c55e55" : "#ef444455", color: town ? "#22c55e" : "#ef4444" }}>
            <Crown className="h-3.5 w-3.5" /> {OUTCOME.headline}
          </div>
          <h2 className="mt-3 text-xl font-semibold text-fg">Roles Unsealed</h2>
          <p className="mx-auto mt-1 max-w-lg text-[13px] text-fg-muted">{OUTCOME.summary}</p>
        </div>

        <motion.div className="mt-6 grid grid-cols-2 gap-3 sm:grid-cols-4" initial="hide" animate="show" variants={{ show: { transition: { staggerChildren: 0.14 } } }}>
          {agents.map((a) => {
            const r = ROLE_META[a.role];
            return (
              <motion.div
                key={a.id}
                variants={{ hide: { opacity: 0, rotateY: 90, y: 10 }, show: { opacity: 1, rotateY: 0, y: 0 } }}
                transition={{ duration: 0.5, ease: [0.22, 1, 0.36, 1] }}
                className="flex flex-col items-center rounded-xl border border-line bg-panel-2/40 p-3"
                style={{ boxShadow: `0 0 0 1px ${r.color}22` }}
              >
                <span className="flex h-11 w-11 items-center justify-center rounded-full border-2 text-base font-bold" style={{ borderColor: a.color, color: a.color }}>
                  {a.name[0]}
                </span>
                <span className="mt-2 text-[13px] font-semibold text-fg">{a.name}</span>
                <span className="font-mono text-[10px] text-fg-muted">{a.dev}</span>
                <span className="mt-2 inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-semibold" style={{ background: `${r.color}1c`, color: r.color }}>
                  <span>{r.glyph}</span> {r.label}
                </span>
              </motion.div>
            );
          })}
        </motion.div>

        <div className="mt-6 flex justify-center gap-3">
          <button onClick={onReplay} className="flex items-center gap-1.5 rounded-lg bg-brand px-4 py-2 text-[13px] font-semibold text-brand-fg shadow transition hover:opacity-90">
            <Play className="h-4 w-4" /> Watch again
          </button>
          <button onClick={onClose} className="rounded-lg border border-line bg-panel px-4 py-2 text-[13px] font-medium text-fg transition hover:border-brand/40">
            Close
          </button>
        </div>
      </motion.div>
    </motion.div>
  );
}

/* ───────────────────────────── shared chrome ───────────────────────────────── */
function Panel({ className, children }: { className?: string; children: React.ReactNode }) {
  return (
    <section className={cn("min-w-0 rounded-xl border border-line bg-panel/80 shadow-[0_1px_2px_rgba(15,23,42,0.04),0_12px_30px_-18px_rgba(15,23,42,0.25)] backdrop-blur", className)}>
      {children}
    </section>
  );
}

function PanelTitle({ icon: Icon, children }: { icon: React.ElementType; children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-2">
      <Icon className="h-3.5 w-3.5 text-fg-muted" />
      <h3 className="text-[11px] font-semibold uppercase tracking-wider text-fg-muted">{children}</h3>
    </div>
  );
}

function Stat({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div>
      <div className="font-mono text-[10px] uppercase tracking-wider text-fg-muted">{k}</div>
      <div className="truncate text-sm font-semibold text-fg">{v}</div>
    </div>
  );
}

function Header({ phase, day, seconds, alive, dead, playing, ended, onToggle }: { phase: Phase; day: number; seconds: number; alive: number; dead: number; playing: boolean; ended: boolean; onToggle: () => void }) {
  return (
    <div className="flex flex-wrap items-center gap-x-5 gap-y-3 rounded-xl border border-line bg-panel/80 px-4 py-3 shadow-sm backdrop-blur">
      <div className="flex items-center gap-2">
        <span className="flex h-7 w-7 items-center justify-center rounded-lg bg-brand text-white">◆</span>
        <div className="leading-tight">
          <p className="text-sm font-semibold text-fg">Pyyol Mafia</p>
          <p className="font-mono text-[10px] text-fg-muted">Season 4 · Match #A3F9</p>
        </div>
      </div>
      <span className="inline-flex items-center gap-1.5 rounded-md border border-brand/30 bg-brand/10 px-2 py-0.5 font-mono text-[10px] font-semibold uppercase tracking-wider text-brand">Ranked</span>
      <span className="inline-flex items-center gap-1.5 rounded-md border border-red-500/30 bg-red-500/10 px-2 py-0.5 font-mono text-[10px] font-semibold uppercase tracking-wider text-red-400">
        <span className="mvx-blink h-1.5 w-1.5 rounded-full bg-red-500" /> {ended ? "Ended" : "Live"}
      </span>

      <div className="flex items-center gap-2 rounded-lg border border-line bg-panel-2/40 px-3 py-1.5">
        <span className="text-sm font-semibold text-fg">Day {day}</span>
        <span className="h-3 w-px bg-line" />
        <span className="font-mono text-[12px] text-fg-muted">{PHASE_LABEL[phase]}</span>
        <span className="h-3 w-px bg-line" />
        <span className="font-mono text-[12px] tabular-nums text-fg-muted">0:{String(seconds).padStart(2, "0")}</span>
      </div>

      <div className="ml-auto flex flex-wrap items-center justify-end gap-x-4 gap-y-2">
        <Metric icon={Users} label="Alive" value={alive} tone="text-emerald-500" />
        <Metric icon={Skull} label="Out" value={dead} tone="text-fg-muted" />
        <Metric icon={Eye} label="Watching" value={"2.4k"} tone="text-brand" />
        <button onClick={onToggle} className="flex items-center gap-1.5 rounded-md border border-line bg-panel px-3 py-1.5 text-[12px] font-medium text-fg shadow-sm transition hover:border-brand/40 hover:text-brand">
          {ended ? <Play className="h-3.5 w-3.5" /> : playing ? <Pause className="h-3.5 w-3.5" /> : <Play className="h-3.5 w-3.5" />}
          {ended ? "Replay" : playing ? "Pause" : "Play"}
        </button>
        <button className="flex items-center gap-1.5 rounded-md border border-line bg-panel px-3 py-1.5 text-[12px] font-medium text-fg shadow-sm transition hover:border-brand/40 hover:text-brand">
          <Share2 className="h-3.5 w-3.5" /> Share
        </button>
      </div>
    </div>
  );
}

function Metric({ icon: Icon, label, value, tone }: { icon: React.ElementType; label: string; value: React.ReactNode; tone: string }) {
  return (
    <div className="flex items-center gap-1.5">
      <Icon className={cn("h-4 w-4", tone)} />
      <span className="text-sm font-semibold text-fg">{value}</span>
      <span className="hidden font-mono text-[10px] uppercase tracking-wider text-fg-muted sm:inline">{label}</span>
    </div>
  );
}

function PhaseBadge({ phase }: { phase: Phase }) {
  const tone =
    phase === "voting"
      ? "border-red-500/30 bg-red-500/10 text-red-400"
      : phase === "night"
        ? "border-brand/30 bg-brand/10 text-brand"
        : phase === "execution"
          ? "border-amber-500/30 bg-amber-500/10 text-amber-400"
          : "border-line bg-panel-2/40 text-fg-muted";
  return (
    <span className={cn("absolute right-4 top-4 z-20 rounded-md border px-2 py-0.5 font-mono text-[10px] font-semibold uppercase tracking-wider", tone)}>
      {PHASE_LABEL[phase]}
    </span>
  );
}

function Inspector({ agent, status, susp, ended, onClose }: { agent: Agent; status: SeatState; susp: number; ended: boolean; onClose: () => void }) {
  const sus = suspicion(susp);
  const role = ROLE_META[agent.role];
  return (
    <div className="fixed inset-0 z-40 flex justify-end" role="dialog" aria-modal="true">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      <div className="relative h-full w-full max-w-sm overflow-y-auto border-l border-line bg-panel p-5 shadow-2xl">
        <div className="flex items-start justify-between">
          <div className="flex items-center gap-3">
            <span className="flex h-12 w-12 items-center justify-center rounded-full border-2 text-lg font-bold" style={{ borderColor: agent.color, color: agent.color }}>
              {agent.name[0]}
            </span>
            <div>
              <p className="text-base font-semibold text-fg">{agent.name}</p>
              <p className="font-mono text-[11px] text-fg-muted">{agent.dev}</p>
            </div>
          </div>
          <button onClick={onClose} className="rounded-md p-1.5 text-fg-muted hover:bg-panel-2 hover:text-fg">
            <X className="h-4 w-4" />
          </button>
        </div>

        <div className="mt-4 flex items-center gap-2">
          <span className="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-semibold" style={{ background: `${STATUS_META[status].color}1c`, color: status === "speaking" ? agent.color : STATUS_META[status].color }}>
            {STATUS_META[status].label}
          </span>
          <span className="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-semibold" style={{ background: `${sus.color}1c`, color: sus.color }}>
            <span className="h-1.5 w-1.5 rounded-full" style={{ background: sus.color }} /> {sus.label}
          </span>
        </div>

        <div className="mt-5 grid grid-cols-2 gap-x-4 gap-y-3">
          <Field k="Win rate" v={`${agent.winRate}%`} />
          <Field k="Games" v={agent.games} />
          <Field k="Response" v={`${agent.responseMs}ms`} />
          <Field k="Model" v={agent.model} />
          <Field k="SDK" v={agent.sdk} />
          <Field k="Manifest" v={`v${agent.manifest}`} />
        </div>

        <div className="mt-6 rounded-lg border border-line bg-panel-2/40 p-3">
          <p className="font-mono text-[10px] uppercase tracking-wider text-fg-muted">Role</p>
          {ended ? (
            <p className="mt-1 inline-flex items-center gap-1.5 text-[13px] font-semibold" style={{ color: role.color }}>
              <span>{role.glyph}</span> {role.label}
            </p>
          ) : (
            <p className="mt-1 text-[13px] text-fg-muted">Concealed until the match ends.</p>
          )}
        </div>
        <p className="mt-3 font-mono text-[10px] text-fg-muted">Hidden roles stay concealed while the match is live.</p>
      </div>
    </div>
  );
}

function Field({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div>
      <div className="font-mono text-[10px] uppercase tracking-wider text-fg-muted">{k}</div>
      <div className="mt-0.5 truncate text-sm font-semibold capitalize text-fg">{v}</div>
    </div>
  );
}
