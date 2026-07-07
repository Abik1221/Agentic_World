"use client";

import * as React from "react";
import { motion, AnimatePresence } from "framer-motion";
import { Clock, Crown, Eye, ListOrdered, Pause, Play, Share2, Trophy, Users, X } from "lucide-react";
import { cn } from "@/lib/cn";
import { DiscussionPanel, STATUS, type DiscussionActivity, type DiscussionMessage } from "@/components/discussion/DiscussionPanel";
import { StrategyTable } from "@/components/table/StrategyTable";
import { GINTENT, type GAgent, type GPhase, type GStep } from "@/lib/goofspiel-demo";
import { useGoofspielLiveScript } from "@/lib/useGoofspielLiveScript";
import { GameOverModal, type Standing } from "@/components/game/GameOverModal";

function goofEventIcon(t: string) {
  const s = t.toLowerCase();
  if (s.includes("prize")) return "🃏";
  if (s.includes("tie")) return "🤝";
  if (s.includes("win") || s.includes("takes") || s.includes("reveal")) return "🏆";
  if (s.includes("lock")) return "🔒";
  return "💬";
}

// Fixed positions around the central stage, in agent-index order (top, right,
// bottom, left). The live 2-player cast fills the first two; the demo fills all
// four. Each entry is the exact wrapper class the corresponding card used before.
const SEAT_SLOTS = [
  "col-start-2 row-start-1 self-start justify-self-center", // top    — agents[0]
  "col-start-3 row-start-2 justify-self-end self-center", // right  — agents[1]
  "col-start-2 row-start-3 self-end justify-self-center", // bottom — agents[2]
  "col-start-1 row-start-2 justify-self-start self-center", // left   — agents[3]
];

const R = 38;
function seatXY(i: number, n: number) {
  const a = (-90 + i * (360 / n)) * (Math.PI / 180);
  return { x: 50 + R * Math.cos(a), y: 50 + R * Math.sin(a) };
}

type ChatMsg = { key: number; agent: GAgent; step: GStep; ts: string };
type HistoryRow = { round: number; pot: number; winner?: string; winningCard: number; tie: boolean; margin: number };

type Derived = {
  round: number;
  phase: GPhase;
  prizeCard: number;
  pot: number;
  hands: Record<string, number[]>;
  scores: Record<string, number>;
  roundsWon: Record<string, number>;
  bids: Record<string, number> | null;
  predict: Record<string, string> | null;
  winner?: string;
  tie: boolean;
  chat: ChatMsg[];
  timeline: { key: number; text: string }[];
  history: HistoryRow[];
  played: Record<string, number[]>; // cards each agent has spent, in order
  streak: Record<string, number>; // current consecutive round wins
};

function replay(upto: number, agents: GAgent[], script: GStep[], hand: number[]): Derived {
  const hands: Record<string, number[]> = {};
  const scores: Record<string, number> = {};
  const roundsWon: Record<string, number> = {};
  agents.forEach((a) => {
    hands[a.id] = [...hand];
    scores[a.id] = 0;
    roundsWon[a.id] = 0;
  });
  let carry = 0,
    prizeCard = 0,
    pot = 0,
    round = 1,
    tie = false;
  let phase: GPhase = "prize";
  let bids: Record<string, number> | null = null;
  let predict: Record<string, string> | null = null;
  let winner: string | undefined;
  const chat: ChatMsg[] = [];
  const timeline: { key: number; text: string }[] = [];
  const history: HistoryRow[] = [];
  const played: Record<string, number[]> = {};
  agents.forEach((a) => (played[a.id] = []));

  for (let i = 0; i <= upto && i < script.length; i++) {
    const s = script[i];
    round = s.round;
    phase = s.phase;
    if (s.phase === "prize") {
      prizeCard = s.prize ?? 0;
      pot = carry + prizeCard;
      bids = null;
      predict = null;
      winner = undefined;
      tie = false;
    }
    if (s.phase === "locked" && s.predict) predict = s.predict;
    if (s.phase === "revealed" && s.bids) {
      bids = s.bids;
      winner = s.winner;
      tie = !!s.tie;
      for (const id of Object.keys(s.bids)) {
        hands[id] = hands[id].filter((c) => c !== s.bids![id]);
        played[id].push(s.bids[id]);
      }
      if (s.tie) carry = pot;
      else if (s.winner) {
        scores[s.winner] += pot;
        roundsWon[s.winner] += 1;
        carry = 0;
      }
      const cards = Object.values(s.bids);
      const sorted = [...cards].sort((a, b) => b - a);
      history.push({
        round: s.round,
        pot,
        winner: s.winner,
        winningCard: s.winner ? s.bids[s.winner] : Math.max(...cards),
        tie: !!s.tie,
        margin: sorted.length > 1 ? sorted[0] - sorted[1] : 0,
      });
    }
    if (s.text && s.speaker) {
      const agent = agents.find((a) => a.id === s.speaker)!;
      chat.push({ key: i, agent, step: s, ts: `R${s.round}` });
    }
    if (s.event) timeline.push({ key: i, text: s.event });
  }

  // current consecutive-win streak per agent, from the round history tail
  const streak: Record<string, number> = {};
  agents.forEach((a) => {
    let s = 0;
    for (let r = history.length - 1; r >= 0; r--) {
      if (history[r].winner === a.id) s++;
      else break;
    }
    streak[a.id] = s;
  });
  return { round, phase, prizeCard, pot, hands, scores, roundsWon, bids, predict, winner, tie, chat, timeline, history, played, streak };
}

const PHASE_LABEL: Record<GPhase, string> = {
  prize: "Prize revealed",
  thinking: "Agents thinking",
  locked: "Decisions locked",
  revealed: "Cards revealed",
};

// persistent play-styles — spectators learn to recognise each agent
const PERSONALITY: Record<string, string> = { A: "Probability Expert", B: "Aggressive", C: "Risk Optimizer", D: "Long-Term Planner" };

function pseudo(seed: number, min: number, max: number) {
  const x = Math.abs(Math.sin(seed * 12.9898) * 43758.5453) % 1;
  return min + x * (max - min);
}
function statusFor(phase: GPhase, isWinner: boolean): string {
  if (phase === "thinking") return "Thinking";
  if (phase === "locked") return "Card Selected";
  if (phase === "revealed") return isWinner ? "Winner" : "Revealed";
  return "Waiting";
}
// live win-probability estimate while agents think (decorative, deterministic)
function predictions(round: number, hands: Record<string, number[]>, agents: GAgent[]): Record<string, number> {
  const raw = agents.map((a) => {
    const hi = Math.max(0, ...(hands[a.id] ?? [0]));
    return { id: a.id, w: hi + pseudo(round * 7 + a.id.charCodeAt(0), 0, 6) };
  });
  const sum = raw.reduce((s, r) => s + r.w, 0) || 1;
  const out: Record<string, number> = {};
  raw.forEach((r) => (out[r.id] = Math.round((r.w / sum) * 100)));
  return out;
}

function CountUp({ value }: { value: number }) {
  const [n, setN] = React.useState(value);
  const prev = React.useRef(value);
  React.useEffect(() => {
    const from = prev.current;
    const to = value;
    prev.current = value;
    if (from === to) return;
    let raf = 0;
    const start = performance.now();
    const tick = (t: number) => {
      const p = Math.min(1, (t - start) / 480);
      setN(Math.round(from + (to - from) * (1 - Math.pow(1 - p, 3))));
      if (p < 1) raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [value]);
  return <>{n}</>;
}

export function GoofspielViewer() {
  const { agents, script, hand } = useGoofspielLiveScript();
  const N = agents.length;
  const TOTAL_ROUNDS = hand.length; // a full match spends the whole hand

  const [idx, setIdx] = React.useState(0);
  const [playing, setPlaying] = React.useState(true);
  const [inspect, setInspect] = React.useState<string | null>(null);
  const [seconds, setSeconds] = React.useState(0);
  const [showFinal, setShowFinal] = React.useState(false);

  const lastIdx = script.length - 1;
  const ended = idx >= lastIdx;
  const d = React.useMemo(() => replay(idx, agents, script, hand), [idx, agents, script, hand]);

  // Cinematic pacing — each reveal lingers; the match holds on the last prize.
  React.useEffect(() => {
    if (!playing || ended) return;
    const cur = script[idx];
    const delay = cur?.phase === "revealed" ? 3600 : cur?.phase === "locked" ? 1600 : cur?.phase === "prize" ? 1700 : 2800;
    const t = setTimeout(() => setIdx((i) => Math.min(i + 1, lastIdx)), delay);
    return () => clearTimeout(t);
  }, [idx, playing, ended, lastIdx]);

  React.useEffect(() => {
    if (!ended) {
      setShowFinal(false);
      return;
    }
    const t = setTimeout(() => setShowFinal(true), 1300);
    return () => clearTimeout(t);
  }, [ended]);

  const replayMatch = () => {
    setShowFinal(false);
    setIdx(0);
    setPlaying(true);
  };

  React.useEffect(() => {
    setSeconds(0);
    if (!playing) return;
    const t = setInterval(() => setSeconds((s) => s + 1), 1000);
    return () => clearInterval(t);
  }, [idx, playing]);

  const ranked = [...agents].sort((a, b) => d.scores[b.id] - d.scores[a.id]);
  const rankOf = (id: string) => ranked.findIndex((a) => a.id === id) + 1;
  const preds = d.phase === "thinking" ? predictions(d.round, d.hands, agents) : null;
  const leader = ranked[0];
  const cardsRemaining = d.hands[agents[0].id]?.length ?? 0;

  const standings = React.useMemo<Standing[]>(() => {
    const top = d.scores[ranked[0]?.id] ?? 0;
    return ranked.map((a, i) => {
      const score = d.scores[a.id] ?? 0;
      const won = d.winner ? a.id === d.winner : i === 0 && score === top;
      return {
        key: a.id,
        name: a.name,
        color: a.color,
        rank: i + 1,
        outcome: (won ? "winner" : "loser") as Standing["outcome"],
        detail: `${score} pts · ${d.roundsWon[a.id] ?? 0}W`,
        sub: a.model,
      };
    });
  }, [ranked, d.scores, d.roundsWon, d.winner]);
  const inspected = agents.find((a) => a.id === inspect);
  const revealed = d.phase === "revealed";

  const messages: DiscussionMessage[] = d.chat.map((m) => ({
    id: m.key,
    agentId: m.agent.id,
    agentName: m.agent.name,
    dev: m.agent.dev,
    color: m.agent.color,
    ts: m.ts,
    text: m.step.text ?? "",
    intent: m.step.intent ? GINTENT[m.step.intent] : undefined,
  }));
  const activity: DiscussionActivity[] = d.timeline.map((t) => ({ id: t.key, icon: goofEventIcon(t.text), text: t.text, ts: `R${d.round}` }));
  const discStatus = (agentId: string) => {
    if (d.phase === "thinking") return STATUS.thinking;
    if (d.phase === "locked") return STATUS.waiting;
    if (d.phase === "revealed") return d.winner === agentId ? STATUS.winner : STATUS.observing;
    return STATUS.observing;
  };
  const typingNames = d.phase === "thinking" ? agents.map((a) => a.name) : [];

  return (
    <div className="mafia-viewer flex min-h-full flex-col gap-4 rounded-xl p-4 text-fg md:p-5">
      {/* Header */}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-3 rounded-xl border border-line bg-panel/80 px-4 py-3 shadow-sm backdrop-blur">
        <div className="flex items-center gap-2">
          <span className="flex h-7 w-7 items-center justify-center rounded-lg bg-brand text-white">♠</span>
          <div className="leading-tight">
            <p className="text-sm font-semibold text-fg">Onavion Goofspiel</p>
            <p className="font-mono text-[10px] text-fg-muted">Season 4 · Match #G7C2</p>
          </div>
        </div>
        <span className="inline-flex items-center gap-1.5 rounded-md border border-red-500/30 bg-red-500/10 px-2 py-0.5 font-mono text-[10px] font-semibold uppercase tracking-wider text-red-400">
          <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-red-500" /> Live
        </span>
        <div className="flex items-center gap-2 rounded-lg border border-line bg-panel-2/40 px-3 py-1.5">
          <span className="text-sm font-semibold text-fg">Round {d.round} / {TOTAL_ROUNDS}</span>
          <span className="h-3 w-px bg-line" />
          <span className="font-mono text-[12px] text-fg-muted">{ended ? "Match complete" : PHASE_LABEL[d.phase]}</span>
          <span className="h-3 w-px bg-line" />
          <span className="font-mono text-[12px] tabular-nums text-fg-muted">0:{String(seconds).padStart(2, "0")}</span>
        </div>
        <div className="ml-auto flex flex-wrap items-center justify-end gap-x-4 gap-y-2">
          <Metric icon={Trophy} label="Leader" value={leader.name} tone="text-amber-400" />
          <Metric icon={ListOrdered} label="Cards left" value={cardsRemaining} tone="text-brand" />
          <Metric icon={Users} label="Players" value={N} tone="text-fg-muted" />
          <Metric icon={Eye} label="Watching" value="1.8k" tone="text-brand" />
          <button
            onClick={() => (ended ? replayMatch() : setPlaying((p) => !p))}
            className="flex items-center gap-1.5 rounded-md border border-line bg-panel px-3 py-1.5 text-[12px] font-medium text-fg shadow-sm transition hover:border-brand/40 hover:text-brand"
          >
            {ended ? <Play className="h-3.5 w-3.5" /> : playing ? <Pause className="h-3.5 w-3.5" /> : <Play className="h-3.5 w-3.5" />}
            {ended ? "Replay" : playing ? "Pause" : "Play"}
          </button>
          <button className="flex items-center gap-1.5 rounded-md border border-line bg-panel px-3 py-1.5 text-[12px] font-medium text-fg shadow-sm transition hover:border-brand/40 hover:text-brand">
            <Share2 className="h-3.5 w-3.5" /> Share
          </button>
        </div>
      </div>

      <div className="grid flex-1 grid-cols-1 gap-4 lg:grid-cols-[1.75fr_1fr]">
        {/* Table + player cards */}
        <Panel className="relative flex items-center justify-center overflow-hidden p-4 lg:h-[560px]">
          <span className="absolute left-4 top-4 z-30 rounded-md border border-line bg-panel-2/40 px-2 py-0.5 font-mono text-[10px] font-semibold uppercase tracking-wider text-fg-muted">
            {ended ? "Match complete" : PHASE_LABEL[d.phase]}
          </span>
          <ActionFeed idx={idx} text={script[idx]?.event} />

          {/* Onavion walnut strategy table — the shared stage, players seated OUTSIDE the rim */}
          <StrategyTable shape="round" size={0.92} topDown className="absolute inset-[15%] z-0" />

          <div className="relative z-10 grid h-full w-full gap-1 [grid-template-columns:auto_minmax(0,1fr)_auto] [grid-template-rows:auto_minmax(0,1fr)_auto]">
            {agents.map((a, i) => (
              <div key={a.id} className={SEAT_SLOTS[i] ?? SEAT_SLOTS[SEAT_SLOTS.length - 1]}>
                <PlayerCard agent={a} d={d} rank={rankOf(a.id)} pred={preds?.[a.id]} onClick={() => setInspect(a.id)} />
              </div>
            ))}
            <div className="col-start-2 row-start-2 h-full w-full">
              <CenterStage d={d} agents={agents} totalRounds={TOTAL_ROUNDS} />
            </div>
          </div>

          {/* End-of-round summary flashes between rounds */}
          <AnimatePresence>{revealed && !ended && <RoundSummary d={d} agents={agents} />}</AnimatePresence>
        </Panel>

        {/* Reasoning */}
        <DiscussionPanel
          title="AI Reasoning"
          messages={messages}
          activity={activity}
          typingNames={typingNames}
          phaseLabel={`Round ${d.round} · ${PHASE_LABEL[d.phase]}`}
          statusOf={discStatus}
          onProfile={setInspect}
          className="lg:h-[560px]"
        />
      </div>

      {/* Footer: timeline · scoreboard · card history */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Panel className="p-4">
          <PanelTitle icon={Clock}>Decision Timeline</PanelTitle>
          <div className="mt-3 space-y-2">
            {d.timeline.slice(-6).reverse().map((t) => (
              <div key={t.key} className="flex items-start gap-2 text-[12px] text-fg-muted">
                <span className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-brand" />
                {t.text}
              </div>
            ))}
          </div>
        </Panel>

        <Panel className="p-4">
          <PanelTitle icon={Trophy}>Scoreboard</PanelTitle>
          <div className="mt-3 space-y-2.5">
            {[...agents].sort((a, b) => d.scores[b.id] - d.scores[a.id]).map((a) => (
              <div key={a.id}>
                <div className="flex items-center justify-between text-[12px]">
                  <span className="flex items-center gap-2 font-medium text-fg">
                    <span className="h-2.5 w-2.5 rounded-full" style={{ background: a.color }} />
                    {a.name}
                  </span>
                  <span className="font-mono text-fg">
                    {d.scores[a.id]} pts · {d.roundsWon[a.id]}W
                  </span>
                </div>
                {/* Hand: remaining highlighted, used faded */}
                <div className="mt-1 flex gap-1">
                  {hand.map((c) => {
                    const left = d.hands[a.id].includes(c);
                    return (
                      <span
                        key={c}
                        className={cn(
                          "flex h-5 w-4 items-center justify-center rounded-sm border font-mono text-[9px] transition",
                          left ? "font-semibold" : "opacity-25 line-through",
                        )}
                        style={{ borderColor: left ? a.color : "#cbd5e1", color: left ? a.color : "#94a3b8" }}
                      >
                        {c}
                      </span>
                    );
                  })}
                </div>
              </div>
            ))}
          </div>
        </Panel>

        <Panel className="p-4">
          <PanelTitle icon={ListOrdered}>Card History</PanelTitle>
          <div className="mt-3 overflow-x-auto">
            <table className="w-full text-[11px]">
              <thead>
                <tr className="font-mono uppercase tracking-wider text-fg-muted">
                  <th className="pb-1.5 text-left font-medium">Rd</th>
                  <th className="pb-1.5 text-left font-medium">Pot</th>
                  <th className="pb-1.5 text-left font-medium">Winner</th>
                  <th className="pb-1.5 text-right font-medium">Card</th>
                  <th className="pb-1.5 text-right font-medium">Margin</th>
                </tr>
              </thead>
              <tbody className="text-fg-muted">
                {d.history.slice().reverse().map((h) => {
                  const w = agents.find((a) => a.id === h.winner);
                  return (
                    <tr key={h.round} className="border-t border-line">
                      <td className="py-1.5 font-mono">{h.round}</td>
                      <td className="py-1.5 font-mono">{h.pot}</td>
                      <td className="py-1.5">
                        {h.tie ? (
                          <span className="font-mono text-amber-400">Tie</span>
                        ) : (
                          <span className="flex items-center gap-1.5 font-medium text-fg">
                            <span className="h-2 w-2 rounded-full" style={{ background: w?.color }} />
                            {w?.name}
                          </span>
                        )}
                      </td>
                      <td className="py-1.5 text-right font-mono font-semibold text-fg">{h.tie ? "—" : h.winningCard}</td>
                      <td className="py-1.5 text-right font-mono">{h.tie ? "—" : `+${h.margin}`}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </Panel>
      </div>

      {inspected && <Inspector agent={inspected} d={d} hand={hand} onClose={() => setInspect(null)} />}

      <GameOverModal
        open={showFinal}
        onClose={() => setShowFinal(false)}
        title="Goofspiel · Match Results"
        banner={`${leader?.name ?? "Winner"} takes the match`}
        standings={standings}
        footer={
          <>
            <button
              onClick={() => setShowFinal(false)}
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

/* ═════════════════════════ premium player card ═════════════════════════════ */
function PlayerCard({ agent, d, rank, pred, onClick }: { agent: GAgent; d: Derived; rank: number; pred?: number; onClick: () => void }) {
  const thinking = d.phase === "thinking";
  const revealed = d.phase === "revealed";
  const isWinner = revealed && d.winner === agent.id;
  const bid = d.bids?.[agent.id];
  const status = statusFor(d.phase, isWinner);
  const streak = d.streak[agent.id] ?? 0;
  const cardsLeft = d.hands[agent.id].length;

  return (
    <button
      onClick={onClick}
      className={cn(
        "w-[124px] rounded-lg border bg-panel/85 p-2 text-left shadow-lg backdrop-blur transition focus:outline-none",
        isWinner ? "mv-winner border-amber-400/60" : thinking ? "gsx-pulse border-brand/40" : "border-line hover:border-brand/40",
      )}
    >
      <div className="flex items-center gap-1.5">
        <span className="relative flex h-7 w-7 shrink-0 items-center justify-center rounded-full border-2 bg-panel text-[11px] font-bold shadow-sm" style={{ borderColor: agent.color, color: agent.color }}>
          {agent.name[0]}
          {rank === 1 && <Crown className="absolute -top-2.5 h-3 w-3 text-amber-400" fill="currentColor" />}
        </span>
        <div className="min-w-0 flex-1">
          <p className="truncate text-[11px] font-semibold leading-tight text-fg">{agent.name}</p>
          <p className="truncate font-mono text-[8px] text-fg-muted">{PERSONALITY[agent.id] ?? "Strategist"}</p>
        </div>
        <span className="shrink-0 rounded bg-panel-2/60 px-1 py-0.5 font-mono text-[8px] font-semibold text-fg-muted">#{rank}</span>
      </div>

      <div className="mt-1.5 flex items-end justify-between">
        <span className="flex items-baseline gap-1">
          <span className="text-[15px] font-bold leading-none text-fg">
            <CountUp value={d.scores[agent.id]} />
          </span>
          <span className="font-mono text-[8px] text-fg-muted">pts</span>
        </span>
        {/* the one status signal on the right: prediction % (thinking) → card (locked/revealed) */}
        {revealed && bid != null ? (
          <span className={cn("gsx-flip flex h-8 w-6 items-center justify-center rounded-md border-2 bg-panel text-[13px] font-bold shadow", !isWinner && "opacity-70")} style={{ borderColor: agent.color, color: agent.color }}>
            {bid}
          </span>
        ) : d.phase === "locked" ? (
          <span className="flex h-8 w-6 items-center justify-center rounded-md border border-line bg-panel-2 text-fg-muted shadow-sm">🂠</span>
        ) : thinking && pred != null ? (
          <span className="flex flex-col items-center rounded-md px-1.5 py-0.5" style={{ background: `${agent.color}18`, color: agent.color }}>
            <span className="text-[13px] font-bold leading-none">{pred}%</span>
            <span className="font-mono text-[6px] uppercase tracking-wider opacity-80">win</span>
          </span>
        ) : (
          <span className="h-8 w-6" />
        )}
      </div>

      <div className="mt-1.5 flex items-center justify-between gap-1 border-t border-line pt-1.5">
        <span className="font-mono text-[8px] text-fg-muted">🃏 {cardsLeft} left</span>
        <span className="flex items-center gap-1">
          {streak >= 2 && <span className="text-[8px]">{"🔥".repeat(Math.min(3, streak))}</span>}
          <span className="inline-flex items-center gap-0.5 font-mono text-[8px] font-semibold uppercase tracking-wide" style={{ color: isWinner ? "#facc15" : thinking ? agent.color : "#8d8da1" }}>
            {thinking && <span className="mv-think-dot h-1 w-1 rounded-full" style={{ background: agent.color }} />}
            {status}
          </span>
        </span>
      </div>
    </button>
  );
}

/* ═════════════════ live action center stage (over the table) ════════════════ */
function CenterStage({ d, agents, totalRounds }: { d: Derived; agents: GAgent[]; totalRounds: number }) {
  const revealed = d.phase === "revealed";
  const thinking = d.phase === "thinking";
  const winner = revealed && d.winner ? agents.find((a) => a.id === d.winner) : undefined;

  return (
    <div className="flex h-full w-full flex-col items-center justify-center gap-2 text-center">
      {/* prize card — rises + rotates in on each new round */}
      <motion.div
        key={`prize-${d.round}`}
        className={cn("gsx-rise relative flex h-24 w-[68px] flex-col items-center justify-center rounded-xl border-2 bg-panel shadow-xl", d.tie && revealed ? "border-amber-500/50" : winner ? "mv-winner border-amber-400/60" : "border-brand/40")}
      >
        <span className="font-mono text-[8px] uppercase tracking-widest text-fg-muted">Prize</span>
        <span className="text-3xl font-bold text-fg">{d.prizeCard}</span>
        {d.pot > d.prizeCard && <span className="rounded-full bg-amber-500/15 px-1.5 font-mono text-[8px] font-semibold text-amber-400">POT {d.pot}</span>}
        {/* winner confetti */}
        {winner && (
          <div className="pointer-events-none absolute inset-0 overflow-visible">
            {[0, 1, 2, 3, 4, 5].map((i) => (
              <span key={i} className="gsx-confetti absolute left-1/2 top-1/2 h-1.5 w-1.5 rounded-[1px]" style={{ background: [winner.color, "#facc15", "#fff"][i % 3], ["--gx" as string]: `${(i - 2.5) * 12}px`, animationDelay: `${i * 60}ms` }} />
            ))}
          </div>
        )}
      </motion.div>

      <span className="font-mono text-[9px] uppercase tracking-wider text-fg-muted">Round {d.round} / {totalRounds}</span>

      {/* phase strip — the center stays clear; per-agent odds live on the cards */}
      <div className="flex min-h-[44px] w-full max-w-[190px] items-center justify-center">
        {thinking ? (
          <span className="inline-flex items-center gap-1.5 rounded-full border border-brand/25 bg-brand/10 px-2.5 py-1 font-mono text-[10px] font-semibold text-brand">
            <span className="mv-think-dot h-1 w-1 rounded-full bg-brand" />
            Agents thinking…
          </span>
        ) : d.phase === "locked" ? (
          <div className="flex h-full items-center justify-center">
            <span className="rounded-full border border-brand/30 bg-brand/10 px-2.5 py-1 font-mono text-[10px] font-semibold text-brand">All cards selected</span>
          </div>
        ) : revealed ? (
          <div className="flex h-full flex-col items-center justify-center gap-0.5">
            {d.tie ? (
              <span className="font-mono text-[11px] font-semibold uppercase tracking-wide text-amber-400">Tie · pot carries</span>
            ) : winner ? (
              <>
                <span className="text-[13px] font-bold" style={{ color: winner.color }}>🏆 {winner.name} wins</span>
                <span className="font-mono text-[10px] font-semibold text-amber-400">+{d.pot} points</span>
              </>
            ) : null}
          </div>
        ) : (
          <div className="flex h-full items-center justify-center">
            <span className="font-mono text-[10px] text-fg-muted">Prize on the table…</span>
          </div>
        )}
      </div>
    </div>
  );
}

/* ═══════════════════ floating action feed (top-center) ═════════════════════ */
function ActionFeed({ idx, text }: { idx: number; text?: string }) {
  if (!text) return null;
  return (
    <AnimatePresence mode="wait">
      <motion.div
        key={idx}
        initial={{ opacity: 0, y: -8 }}
        animate={{ opacity: 1, y: 0 }}
        exit={{ opacity: 0, y: -8 }}
        transition={{ duration: 0.24 }}
        className="absolute left-1/2 top-3 z-30 -translate-x-1/2 rounded-full border border-line bg-panel/85 px-3 py-1 shadow-lg backdrop-blur"
      >
        <span className="font-mono text-[11px] font-medium text-fg">{goofEventIcon(text)} {text}</span>
      </motion.div>
    </AnimatePresence>
  );
}

/* ═══════════════════════ end-of-round summary flash ════════════════════════ */
function RoundSummary({ d, agents }: { d: Derived; agents: GAgent[] }) {
  const winner = d.winner ? agents.find((a) => a.id === d.winner) : undefined;
  const leader = [...agents].sort((a, b) => d.scores[b.id] - d.scores[a.id])[0];
  return (
    <motion.div
      initial={{ opacity: 0, scale: 0.96, y: 8 }}
      animate={{ opacity: 1, scale: 1, y: 0 }}
      exit={{ opacity: 0, scale: 0.98 }}
      transition={{ duration: 0.28 }}
      className="absolute bottom-4 left-1/2 z-30 w-[240px] -translate-x-1/2 rounded-xl border border-line bg-panel/90 p-3 shadow-2xl backdrop-blur"
    >
      <p className="text-center font-mono text-[9px] uppercase tracking-wider text-fg-muted">Round {d.round} complete · Prize {d.prizeCard}</p>
      <div className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1">
        {agents.map((a) => (
          <div key={a.id} className="flex items-center justify-between text-[11px]">
            <span className="flex items-center gap-1.5 text-fg-muted">
              <span className="h-2 w-2 rounded-full" style={{ background: a.color }} />
              {a.name}
            </span>
            <span className="font-mono font-semibold text-fg">{d.bids?.[a.id] ?? "—"}</span>
          </div>
        ))}
      </div>
      <div className="mt-2 flex items-center justify-between border-t border-line pt-2 text-[11px]">
        <span className="text-fg-muted">
          Winner <span className="font-semibold" style={{ color: winner?.color }}>{d.tie ? "Tie" : winner?.name ?? "—"}</span>
        </span>
        <span className="text-fg-muted">
          Leader <span className="font-semibold text-amber-400">{leader.name}</span>
        </span>
      </div>
    </motion.div>
  );
}

/* ═════════════════════════ final match celebration ═════════════════════════ */
function FinalCelebration({ d, ranked, onReplay, onClose }: { d: Derived; ranked: GAgent[]; onReplay: () => void; onClose: () => void }) {
  const champ = ranked[0];
  const totalWins = d.roundsWon[champ.id] ?? 0;
  return (
    <motion.div className="fixed inset-0 z-50 flex items-center justify-center p-4" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} role="dialog" aria-modal="true">
      <div className="absolute inset-0 bg-black/80 backdrop-blur-md" onClick={onClose} />
      <motion.div className="relative w-full max-w-lg rounded-2xl border border-line bg-panel/90 p-6 shadow-2xl backdrop-blur" initial={{ scale: 0.94, y: 16 }} animate={{ scale: 1, y: 0 }} transition={{ type: "spring", stiffness: 240, damping: 26 }}>
        <button onClick={onClose} className="absolute right-4 top-4 rounded-md p-1.5 text-fg-muted hover:bg-panel-2 hover:text-fg">
          <X className="h-4 w-4" />
        </button>
        {/* confetti burst */}
        <div className="pointer-events-none absolute inset-x-0 top-0 h-24 overflow-hidden">
          {Array.from({ length: 14 }).map((_, i) => (
            <span key={i} className="gsx-confetti absolute top-6 h-2 w-2 rounded-[2px]" style={{ left: `${8 + i * 6.4}%`, background: [champ.color, "#facc15", "#6366f1", "#22c55e"][i % 4], ["--gx" as string]: `${(i % 5 - 2) * 16}px`, animationDelay: `${i * 70}ms`, animationDuration: "2.2s" }} />
          ))}
        </div>
        <div className="text-center">
          <div className="inline-flex items-center gap-2 rounded-full border border-amber-400/40 px-3 py-1 font-mono text-[11px] uppercase tracking-wider text-amber-400">
            <Crown className="h-3.5 w-3.5" fill="currentColor" /> Champion
          </div>
          <div className="mt-3 flex items-center justify-center gap-3">
            <span className="flex h-14 w-14 items-center justify-center rounded-full border-2 text-xl font-bold" style={{ borderColor: champ.color, color: champ.color }}>
              {champ.name[0]}
            </span>
            <div className="text-left">
              <p className="text-xl font-semibold text-fg">{champ.name}</p>
              <p className="font-mono text-[11px] text-fg-muted">{champ.dev} · {PERSONALITY[champ.id] ?? "Strategist"}</p>
            </div>
          </div>
          <p className="mt-2 font-mono text-[13px] font-semibold text-amber-400">{d.scores[champ.id]} points · {totalWins} round wins</p>
        </div>

        <div className="mt-5 space-y-1.5">
          {ranked.map((a, i) => (
            <div key={a.id} className="flex items-center gap-3 rounded-lg border border-line bg-panel-2/40 px-3 py-2">
              <span className="w-5 font-mono text-[12px] font-semibold text-fg-muted">{i + 1}</span>
              <span className="h-2.5 w-2.5 rounded-full" style={{ background: a.color }} />
              <span className="flex-1 text-[13px] font-medium text-fg">{a.name}</span>
              <span className="font-mono text-[11px] text-fg-muted">{d.roundsWon[a.id] ?? 0}W</span>
              <span className="w-14 text-right font-mono text-[13px] font-semibold text-fg">{d.scores[a.id]} pts</span>
            </div>
          ))}
        </div>

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

/* ───────────────────────────── sub-components ─────────────────────────────── */
function Panel({ className, children }: { className?: string; children: React.ReactNode }) {
  return (
    <section className={cn("rounded-xl border border-line bg-panel/80 min-w-0 shadow-[0_1px_2px_rgba(15,23,42,0.04),0_12px_30px_-18px_rgba(15,23,42,0.25)] backdrop-blur", className)}>
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
function Metric({ icon: Icon, label, value, tone }: { icon: React.ElementType; label: string; value: React.ReactNode; tone: string }) {
  return (
    <div className="flex items-center gap-1.5">
      <Icon className={cn("h-4 w-4", tone)} />
      <span className="max-w-[90px] truncate text-sm font-semibold text-fg">{value}</span>
      <span className="hidden font-mono text-[10px] uppercase tracking-wider text-fg-muted sm:inline">{label}</span>
    </div>
  );
}

function Inspector({ agent, d, hand, onClose }: { agent: GAgent; d: Derived; hand: number[]; onClose: () => void }) {
  const used = hand.filter((c) => !d.hands[agent.id].includes(c));
  return (
    <div className="fixed inset-0 z-50 flex justify-end" role="dialog" aria-modal="true">
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
        <div className="mt-5 grid grid-cols-2 gap-x-4 gap-y-3">
          <Field k="Score" v={`${d.scores[agent.id]} pts`} />
          <Field k="Rounds won" v={d.roundsWon[agent.id]} />
          <Field k="Cards left" v={d.hands[agent.id].length} />
          <Field k="Prediction acc" v={`${agent.predictAcc}%`} />
          <Field k="Win rate" v={`${agent.winRate}%`} />
          <Field k="Avg think" v={`${agent.avgThinkMs}ms`} />
          <Field k="Model" v={agent.model} />
          <Field k="SDK" v={`v${agent.sdk}`} />
        </div>
        <div className="mt-5">
          <p className="font-mono text-[10px] uppercase tracking-wider text-fg-muted">Cards used</p>
          <div className="mt-1.5 flex gap-1">
            {hand.map((c) => {
              const isUsed = used.includes(c);
              return (
                <span
                  key={c}
                  className={cn("flex h-6 w-5 items-center justify-center rounded-sm border font-mono text-[10px]", isUsed ? "opacity-30 line-through" : "font-semibold")}
                  style={{ borderColor: isUsed ? "#cbd5e1" : agent.color, color: isUsed ? "#94a3b8" : agent.color }}
                >
                  {c}
                </span>
              );
            })}
          </div>
        </div>
      </div>
    </div>
  );
}

function Field({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div>
      <div className="font-mono text-[10px] uppercase tracking-wider text-fg-muted">{k}</div>
      <div className="mt-0.5 truncate text-sm font-semibold text-fg">{v}</div>
    </div>
  );
}
