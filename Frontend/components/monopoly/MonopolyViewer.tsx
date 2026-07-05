"use client";

import * as React from "react";
import { motion, AnimatePresence } from "framer-motion";
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { ArrowRight, Clock, Coins, Crown, Eye, Home, ListOrdered, Pause, Play, Radio, Share2, TrendingUp, Users, X } from "lucide-react";
import { cn } from "@/lib/cn";
import { DiscussionPanel, STATUS, type DiscussionActivity, type DiscussionMessage } from "@/components/discussion/DiscussionPanel";
import { StrategyTable } from "@/components/table/StrategyTable";
import { AreaTrend, CHART } from "@/components/console/charts";
import {
  BOARD,
  gridPos,
  INITIAL_OWNERS,
  MAGENTS,
  MINTENT,
  MSCRIPT,
  type MAgent,
  type MStep,
  type Space,
} from "@/lib/monopoly-demo";

const HOUSE = 50;
const HOTEL = 5; // houses >= 5 render as a hotel
const priceOf = (i: number) => BOARD[i]?.price ?? 0;
const money = (n: number) => `$${Math.round(n).toLocaleString()}`;

// rent model (mock but internally consistent): base 8% of price, +50% per house.
function rentOf(i: number, houses: number) {
  const p = priceOf(i);
  if (!p) return 0;
  if (houses >= HOTEL) return Math.round(p * 1.6);
  return Math.round(p * 0.08 + houses * p * 0.5);
}

// property color groups (for monopoly detection + connectors)
const GROUPS: Record<string, number[]> = (() => {
  const g: Record<string, number[]> = {};
  BOARD.forEach((s) => {
    if (s.type === "prop" && s.group) (g[s.group] ??= []).push(s.i);
  });
  return g;
})();

function cellCenterPct(i: number) {
  const { col, row } = gridPos(i);
  return { x: ((col - 0.5) / 11) * 100, y: ((row - 0.5) / 11) * 100 };
}

const OBJECTIVE: Record<string, string> = {
  planning: "Mapping best mid-board ROI",
  roi: "Expected ROI ~17%",
  trade: "Seeking a value swap",
  threat: "Blocking a rival monopoly",
  liquidity: "Protecting cash reserves",
  build: "Spiking rent with houses",
  expansion: "Expanding the portfolio",
  monopoly: "Completing a color set",
  defensive: "Hoarding cash, waiting",
};

type ChatMsg = { key: number; agent: MAgent; step: MStep; ts: string };
type Activity = { key: number; kind: MStep["kind"]; text: string; turn: number; color: string; icon: string };
type NetPoint = { label: string } & Record<string, number | string>;

type Derived = {
  turn: number;
  player: string;
  dice: [number, number] | null;
  positions: Record<string, number>;
  cash: Record<string, number>;
  owners: Record<number, string>;
  houses: Record<number, number>;
  mortgaged: Record<number, boolean>;
  spaceLanded: Record<number, number>;
  spaceIncome: Record<number, number>;
  chat: ChatMsg[];
  activity: Activity[];
  timeline: { key: number; text: string }[];
  netHistory: { label: string; value: number }[];
  netSeries: NetPoint[];
  tradesDone: number;
  tradesRejected: number;
};

const ACT_ICON: Record<MStep["kind"], string> = { say: "💬", roll: "🎲", buy: "🏷", rent: "💸", trade: "🤝", build: "🏗" };

function replay(upto: number): Derived {
  const positions: Record<string, number> = {};
  const cash: Record<string, number> = {};
  MAGENTS.forEach((a) => {
    positions[a.id] = 0;
    cash[a.id] = a.startCash;
  });
  const owners: Record<number, string> = { ...INITIAL_OWNERS };
  const houses: Record<number, number> = {};
  const mortgaged: Record<number, boolean> = {};
  const spaceLanded: Record<number, number> = {};
  const spaceIncome: Record<number, number> = {};
  const chat: ChatMsg[] = [];
  const activity: Activity[] = [];
  const timeline: { key: number; text: string }[] = [];
  const netHistory: { label: string; value: number }[] = [];
  const netSeries: NetPoint[] = [];
  let turn = MSCRIPT[0]?.turn ?? 1;
  let player = MAGENTS[0].id;
  let dice: [number, number] | null = null;
  let tradesDone = 0;
  let tradesRejected = 0;

  const netWorth = (id: string) => {
    let n = cash[id];
    for (const k of Object.keys(owners)) if (owners[+k] === id) n += priceOf(+k) + (houses[+k] ?? 0) * HOUSE;
    return n;
  };
  const nameOf = (id: string) => MAGENTS.find((a) => a.id === id)?.name ?? id;
  const colorOf = (id: string) => MAGENTS.find((a) => a.id === id)?.color ?? "#888";

  for (let i = 0; i <= upto && i < MSCRIPT.length; i++) {
    const s = MSCRIPT[i];
    turn = s.turn;
    player = s.player;

    if (s.kind === "roll" && s.dice) {
      dice = s.dice;
      if (s.to != null) {
        positions[s.player] = s.to;
        spaceLanded[s.to] = (spaceLanded[s.to] ?? 0) + 1;
      }
    }
    if (s.kind === "buy" && s.buy != null) {
      owners[s.buy] = s.player;
      cash[s.player] -= priceOf(s.buy);
    }
    if (s.kind === "rent" && s.rent) {
      cash[s.rent.from] -= s.rent.amount;
      cash[s.rent.to] += s.rent.amount;
      const pos = positions[s.rent.from];
      if (pos != null) spaceIncome[pos] = (spaceIncome[pos] ?? 0) + s.rent.amount;
    }
    if (s.kind === "trade" && s.trade) {
      if (s.trade.status === "accept") {
        tradesDone++;
        const t = s.trade;
        if (t.giveIdx != null) owners[t.giveIdx] = t.to;
        if (t.getIdx != null) owners[t.getIdx] = t.from;
        if (t.cash) {
          cash[t.from] -= t.cash;
          cash[t.to] += t.cash;
        }
      }
      if (s.trade.status === "reject") tradesRejected++;
    }
    if (s.kind === "build" && s.build) {
      for (const sp of s.build.spaces) houses[sp] = Math.min(HOTEL, (houses[sp] ?? 0) + 1);
    }

    if (s.text && s.speaker) chat.push({ key: i, agent: MAGENTS.find((a) => a.id === s.speaker)!, step: s, ts: `T${s.turn}` });

    // activity feed entry
    let atext = "";
    if (s.kind === "roll" && s.dice) atext = `${nameOf(s.player)} rolled ${s.dice[0] + s.dice[1]} → ${BOARD[s.to ?? 0]?.name}`;
    else if (s.kind === "buy" && s.buy != null) atext = `${nameOf(s.player)} bought ${BOARD[s.buy].name} (${money(priceOf(s.buy))})`;
    else if (s.kind === "rent" && s.rent) atext = `${nameOf(s.rent.from)} paid ${money(s.rent.amount)} rent to ${nameOf(s.rent.to)}`;
    else if (s.kind === "trade" && s.trade) atext = s.trade.status === "propose" ? `${nameOf(s.trade.from)} proposed a trade to ${nameOf(s.trade.to)}` : s.trade.status === "accept" ? `Trade accepted: ${s.trade.give} ↔ ${s.trade.get}` : `${nameOf(s.trade.from)} rejected the trade`;
    else if (s.kind === "build" && s.build) atext = `${nameOf(s.player)} built on ${Array.from(new Set(s.build.spaces)).map((x) => BOARD[x].name).join(", ")}`;
    if (atext) activity.push({ key: i, kind: s.kind, text: atext, turn: s.turn, color: colorOf(s.player), icon: ACT_ICON[s.kind] });

    if (s.event) timeline.push({ key: i, text: s.event });

    const point: NetPoint = { label: `${i + 1}` };
    let total = 0;
    for (const a of MAGENTS) {
      const nw = netWorth(a.id);
      point[a.id] = nw;
      total += nw;
    }
    netSeries.push(point);
    netHistory.push({ label: `#${i + 1}`, value: total });
  }

  return { turn, player, dice, positions, cash, owners, houses, mortgaged, spaceLanded, spaceIncome, chat, activity, timeline, netHistory, netSeries, tradesDone, tradesRejected };
}

/* active trade for the centered overlay (pending, or the resolving frame) */
function tradeAt(idx: number) {
  let t: MStep["trade"] | null = null;
  let resolvedAt = -1;
  let result: "accept" | "reject" | null = null;
  for (let i = 0; i <= idx; i++) {
    const tr = MSCRIPT[i].trade;
    if (tr?.status === "propose") {
      t = tr;
      resolvedAt = -1;
      result = null;
    } else if (tr && (tr.status === "accept" || tr.status === "reject") && t) {
      resolvedAt = i;
      result = tr.status;
    }
  }
  if (!t) return null;
  if (resolvedAt < 0) return { trade: t, result: null as null | "accept" | "reject" };
  if (idx === resolvedAt) return { trade: t, result };
  return null;
}

function nextThinking(idx: number): string[] {
  const names: string[] = [];
  for (let i = idx + 1; i < MSCRIPT.length && names.length < 2; i++) {
    const s = MSCRIPT[i];
    if (s.speaker && s.text) {
      const n = MAGENTS.find((a) => a.id === s.speaker)?.name;
      if (n && !names.includes(n)) names.push(n);
    }
  }
  return names;
}

type Tab = "live" | "standings" | "replays";

/* ═══════════════════════════════ viewer ════════════════════════════════════ */
export function MonopolyViewer() {
  const [idx, setIdx] = React.useState(0);
  const [playing, setPlaying] = React.useState(true);
  const [tab, setTab] = React.useState<Tab>("live");
  const [inspect, setInspect] = React.useState<string | null>(null);
  const d = React.useMemo(() => replay(idx), [idx]);

  React.useEffect(() => {
    if (!playing) return;
    const cur = MSCRIPT[idx];
    const isTrade = cur?.trade?.status === "propose";
    const delay = isTrade ? 3800 : cur?.kind === "roll" ? 2200 : cur?.kind === "say" ? 3100 : 2500;
    const t = setTimeout(() => setIdx((i) => (i + 1) % MSCRIPT.length), delay);
    return () => clearTimeout(t);
  }, [idx, playing]);

  const netWorth = React.useCallback(
    (id: string) => {
      let n = d.cash[id];
      for (const k of Object.keys(d.owners)) if (d.owners[+k] === id) n += priceOf(+k) + (d.houses[+k] ?? 0) * HOUSE;
      return n;
    },
    [d],
  );
  const ranked = [...MAGENTS].sort((a, b) => netWorth(b.id) - netWorth(a.id));
  const maxNet = Math.max(...MAGENTS.map((a) => netWorth(a.id)), 1);
  const propsOf = React.useCallback((id: string) => Object.keys(d.owners).filter((k) => d.owners[+k] === id).map(Number), [d]);
  const inspected = MAGENTS.find((a) => a.id === inspect);
  const cur = MAGENTS.find((a) => a.id === d.player)!;
  const step = MSCRIPT[idx];
  const trade = tradeAt(idx);
  const thinking = nextThinking(idx);

  const messages: DiscussionMessage[] = d.chat.map((m) => {
    const tr = m.step.trade;
    return {
      id: m.key,
      agentId: m.agent.id,
      agentName: m.agent.name,
      dev: m.agent.dev,
      color: m.agent.color,
      ts: m.ts,
      text: m.step.text ?? "",
      intent: m.step.intent ? MINTENT[m.step.intent] : undefined,
      extra:
        tr && tr.status === "propose" ? (
          <div className="mt-1 flex items-center gap-2 rounded-lg border border-brand/20 bg-brand/10 px-2.5 py-1.5 text-[11px] text-brand">
            <span className="font-medium">{tr.give}</span>
            <ArrowRight className="h-3 w-3" />
            <span className="font-medium">{tr.get}</span>
          </div>
        ) : undefined,
    };
  });
  const activityItems: DiscussionActivity[] = d.activity.map((a) => ({ id: a.key, icon: a.icon, text: a.text, ts: `T${a.turn}` }));
  const stepStatusKey =
    step.kind === "trade" ? "negotiating" : step.kind === "roll" ? "thinking" : step.kind === "buy" || step.kind === "build" ? "analyzing" : step.kind === "rent" ? "waiting" : step.intent === "trade" ? "negotiating" : step.intent === "roi" || step.intent === "planning" ? "analyzing" : "thinking";
  const discStatus = (id: string) => (id === d.player ? STATUS[stepStatusKey] : STATUS.observing);

  // monopolies per player + the set of "hot" spaces from the current step
  const monopolies = React.useMemo(() => {
    const owned = new Set<number>();
    for (const [, spaces] of Object.entries(GROUPS)) {
      const os = spaces.map((i) => d.owners[i]);
      if (os[0] && os.every((o) => o === os[0])) spaces.forEach((i) => owned.add(i));
    }
    return owned;
  }, [d]);
  const hotSpaces = React.useMemo(() => {
    if (step.kind === "buy" && step.buy != null) return [step.buy];
    if (step.kind === "build" && step.build) return Array.from(new Set(step.build.spaces));
    if (step.kind === "rent" && step.rent) return [d.positions[step.rent.from]];
    if (step.kind === "roll" && step.to != null) return [step.to];
    return [];
  }, [step, d]);

  return (
    <div className="mafia-viewer flex min-h-full flex-col gap-4 rounded-xl p-4 text-fg md:p-5">
      {/* Header */}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-3 rounded-xl border border-line bg-panel/80 px-4 py-3 shadow-sm backdrop-blur">
        <div className="flex items-center gap-2">
          <span className="flex h-7 w-7 items-center justify-center rounded-lg bg-brand text-white">⬢</span>
          <div className="leading-tight">
            <p className="text-sm font-semibold text-fg">Onavion Monopoly</p>
            <p className="font-mono text-[10px] text-fg-muted">Season 4 · Match #M2K8</p>
          </div>
        </div>
        <span className="inline-flex items-center gap-1.5 rounded-md border border-red-500/30 bg-red-500/10 px-2 py-0.5 font-mono text-[10px] font-semibold uppercase tracking-wider text-red-400">
          <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-red-500" /> Live
        </span>
        <div className="flex items-center gap-2 rounded-lg border border-line bg-panel-2/40 px-3 py-1.5">
          <span className="text-sm font-semibold text-fg">Turn {d.turn}</span>
          <span className="h-3 w-px bg-line" />
          <span className="flex items-center gap-1.5 font-mono text-[12px] text-fg-muted">
            <span className="h-2 w-2 rounded-full" style={{ background: cur.color }} />
            {cur.name}
          </span>
        </div>
        <div className="ml-auto flex flex-wrap items-center justify-end gap-x-4 gap-y-2">
          <Metric icon={Crown} label="Leader" value={ranked[0].name} tone="text-amber-400" />
          <Metric icon={Users} label="Players" value={MAGENTS.length} tone="text-fg-muted" />
          <Metric icon={Eye} label="Watching" value="3.1k" tone="text-brand" />
          <button onClick={() => setPlaying((p) => !p)} className="flex items-center gap-1.5 rounded-md border border-line bg-panel px-3 py-1.5 text-[12px] font-medium text-fg shadow-sm transition hover:border-brand/40 hover:text-brand">
            {playing ? <Pause className="h-3.5 w-3.5" /> : <Play className="h-3.5 w-3.5" />}
            {playing ? "Pause" : "Play"}
          </button>
          <button className="flex items-center gap-1.5 rounded-md border border-line bg-panel px-3 py-1.5 text-[12px] font-medium text-fg shadow-sm transition hover:border-brand/40 hover:text-brand">
            <Share2 className="h-3.5 w-3.5" /> Share
          </button>
        </div>
      </div>

      <SubTabs tab={tab} onSelect={setTab} />

      {tab === "standings" ? (
        <Standings ranked={ranked} netWorth={netWorth} cash={d.cash} propsOf={propsOf} houses={d.houses} netHistory={d.netHistory} onInspect={setInspect} />
      ) : tab === "replays" ? (
        <Panel className="p-6">
          <PanelTitle icon={Clock}>Replays</PanelTitle>
          <div className="mt-3 space-y-2">
            {[1, 2, 3].map((n) => (
              <div key={n} className="flex items-center justify-between rounded-lg border border-line bg-panel-2/40 px-4 py-3">
                <span className="text-[13px] font-medium text-fg">Match #M2K{n} · Season 4</span>
                <button className="flex items-center gap-1.5 rounded-md border border-line bg-panel px-3 py-1.5 text-[12px] text-fg-muted hover:border-brand/40 hover:text-brand">
                  <Play className="h-3.5 w-3.5" /> Watch replay
                </button>
              </div>
            ))}
          </div>
        </Panel>
      ) : (
        <>
          <div className="grid flex-1 grid-cols-1 gap-4 lg:grid-cols-[1.7fr_1fr]">
            <Panel className="mnx-board relative flex items-center justify-center overflow-hidden p-4 lg:h-[600px]">
              <LiveEventToast idx={idx} text={step.event} />
              <Board d={d} cur={cur} step={step} hotSpaces={hotSpaces} monopolies={monopolies} onInspect={setInspect} />
            </Panel>
            <DiscussionPanel
              title="AI Negotiation"
              messages={messages}
              activity={activityItems}
              typingNames={thinking}
              phaseLabel={`Turn ${d.turn} · ${cur.name}`}
              statusOf={discStatus}
              onProfile={setInspect}
              className="lg:h-[600px]"
            />
          </div>

          {/* Footer: Portfolio & ranking · Net worth graph · Statistics */}
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
            <Panel className="p-4">
              <PanelTitle icon={Coins}>Portfolio & Ranking</PanelTitle>
              <div className="mt-3 space-y-2">
                {ranked.map((a, i) => (
                  <PortfolioCard key={a.id} agent={a} rank={i} cash={d.cash[a.id]} net={netWorth(a.id)} props={propsOf(a.id)} houses={d.houses} chat={d.chat} onClick={() => setInspect(a.id)} />
                ))}
              </div>
            </Panel>

            <Panel className="p-4">
              <PanelTitle icon={TrendingUp}>Net Worth Over Time</PanelTitle>
              <div className="mt-3 h-[240px]">
                <NetWorthGraph data={d.netSeries} />
              </div>
            </Panel>

            <Panel className="p-4">
              <PanelTitle icon={Eye}>Statistics</PanelTitle>
              <div className="mt-3 grid grid-cols-2 gap-x-4 gap-y-2.5 text-[12px]">
                <Stat k="Turn" v={d.turn} />
                <Stat k="Leader" v={ranked[0].name} />
                <Stat k="Trades done" v={d.tradesDone} />
                <Stat k="Trades rejected" v={d.tradesRejected} />
                <Stat k="Houses built" v={Object.values(d.houses).reduce((s, n) => s + n, 0)} />
                <Stat k="Monopolies" v={new Set(Array.from(monopolies).map((i) => BOARD[i].group)).size} />
              </div>
              <div className="mt-4">
                <p className="font-mono text-[10px] uppercase tracking-wider text-fg-muted">Wealth ranking</p>
                <div className="mt-2 space-y-1.5">
                  {ranked.map((a) => (
                    <div key={a.id} className="flex items-center gap-2">
                      <span className="w-12 shrink-0 truncate text-[11px] text-fg-muted">{a.name}</span>
                      <div className="h-2 flex-1 overflow-hidden rounded-full bg-panel-2">
                        <motion.div className="h-full rounded-full" style={{ background: a.color }} animate={{ width: `${(netWorth(a.id) / maxNet) * 100}%` }} transition={{ type: "spring", stiffness: 160, damping: 24 }} />
                      </div>
                      <span className="w-14 shrink-0 text-right font-mono text-[10px] text-fg-muted">{money(netWorth(a.id))}</span>
                    </div>
                  ))}
                </div>
              </div>
            </Panel>
          </div>
        </>
      )}

      {inspected && <Inspector agent={inspected} d={d} netWorth={netWorth(inspected.id)} props={propsOf(inspected.id)} monopolies={monopolies} onClose={() => setInspect(null)} />}

      <AnimatePresence>{trade && <TradeOverlay trade={trade.trade} result={trade.result} />}</AnimatePresence>
    </div>
  );
}

/* ─────────────────────────────── board ─────────────────────────────── */
function Board({
  d,
  cur,
  step,
  hotSpaces,
  monopolies,
  onInspect,
}: {
  d: Derived;
  cur: MAgent;
  step: MStep;
  hotSpaces: number[];
  monopolies: Set<number>;
  onInspect: (id: string) => void;
}) {
  const [hover, setHover] = React.useState<number | null>(null);
  return (
    <div className="relative mx-auto aspect-square w-full max-w-[560px]">
      {/* Onavion walnut strategy table — the board sits on top of it */}
      <StrategyTable shape="square" size={0.99} className="absolute -inset-[4%]" />
      <div className="relative grid h-full w-full grid-cols-11 grid-rows-11 gap-1 rounded-2xl border border-line/60 p-1.5">
        {BOARD.map((sp) => {
          const { col, row } = gridPos(sp.i);
          const owner = d.owners[sp.i];
          const ownerAgent = MAGENTS.find((a) => a.id === owner);
          const houses = d.houses[sp.i] ?? 0;
          return (
            <div key={sp.i} style={{ gridColumn: col, gridRow: row }} className="relative" onMouseEnter={() => setHover(sp.i)} onMouseLeave={() => setHover((h) => (h === sp.i ? null : h))}>
              <BoardCell sp={sp} ownerColor={ownerAgent?.color} houses={houses} hot={hotSpaces.includes(sp.i)} mono={monopolies.has(sp.i)} onClick={() => owner && onInspect(owner)} />
            </div>
          );
        })}

        {/* Center */}
        <div style={{ gridColumn: "2 / 11", gridRow: "2 / 11" }} className="flex flex-col items-center justify-center gap-3 rounded-2xl">
          <CenterStage d={d} cur={cur} step={step} />
        </div>
      </div>

      {/* animated player tokens */}
      <TokenLayer positions={d.positions} />

      {/* hover detail popover */}
      <AnimatePresence>{hover != null && BOARD[hover].type === "prop" && <HoverCard sp={BOARD[hover]} d={d} />}</AnimatePresence>
    </div>
  );
}

function BoardCell({ sp, ownerColor, houses, hot, mono, onClick }: { sp: Space; ownerColor?: string; houses: number; hot: boolean; mono: boolean; onClick: () => void }) {
  const special = sp.type !== "prop";
  const hotel = houses >= HOTEL;
  return (
    <button
      onClick={onClick}
      className={cn(
        "group relative flex h-full min-h-[34px] w-full flex-col overflow-hidden rounded-lg border bg-gradient-to-b from-panel to-panel-2/60 text-left shadow-[0_1px_2px_rgba(0,0,0,0.4)] transition",
        ownerColor ? "border-transparent ring-2" : "border-line/70",
        hot && "mnx-activity",
      )}
      style={
        {
          ...(ownerColor ? ({ ["--tw-ring-color" as string]: ownerColor } as React.CSSProperties) : {}),
          ...(hot ? ({ ["--mnx-c" as string]: `${ownerColor ?? "#6366f1"}bb` } as React.CSSProperties) : {}),
        } as React.CSSProperties
      }
      title={sp.name + (sp.price ? ` · $${sp.price}` : "")}
    >
      {!special && <span className={cn("h-1.5 w-full shrink-0", mono && "mnx-mono")} style={{ background: sp.group }} />}
      <div className="flex flex-1 flex-col justify-between p-0.5">
        <span className="line-clamp-2 text-[7px] font-medium leading-tight text-fg-muted">{sp.name}</span>
        {sp.price ? <span className="font-mono text-[7px] text-fg-muted">${sp.price}</span> : null}
      </div>
      {/* houses / hotel */}
      {houses > 0 && (
        <div className="absolute right-0.5 top-1.5 flex gap-px">
          {hotel ? (
            <span className="mnx-pop text-[9px] leading-none">🏨</span>
          ) : (
            Array.from({ length: houses }).map((_, i) => <Home key={i} className="mnx-pop h-1.5 w-1.5 text-emerald-400" style={{ animationDelay: `${i * 60}ms` }} />)
          )}
        </div>
      )}
      {/* owner dot */}
      {ownerColor && <span className="absolute bottom-0.5 left-0.5 h-1.5 w-1.5 rounded-full ring-1 ring-black/40" style={{ background: ownerColor }} />}
      {mono && <span className="mnx-mono pointer-events-none absolute inset-0 rounded-lg ring-1" style={{ ["--tw-ring-color" as string]: `${sp.group}88` } as React.CSSProperties} />}
    </button>
  );
}

function TokenLayer({ positions }: { positions: Record<string, number> }) {
  // group tokens sharing a space to offset them
  const bySpace: Record<number, string[]> = {};
  MAGENTS.forEach((a) => (bySpace[positions[a.id]] ??= []).push(a.id));
  return (
    <div className="pointer-events-none absolute inset-1.5">
      {MAGENTS.map((a) => {
        const pos = positions[a.id];
        const { x, y } = cellCenterPct(pos);
        const mates = bySpace[pos];
        const k = mates.indexOf(a.id);
        const off = (k - (mates.length - 1) / 2) * 11;
        return (
          <motion.div
            key={a.id}
            className="absolute -ml-3 -mt-3"
            animate={{ left: `${x}%`, top: `${y}%` }}
            transition={{ type: "spring", stiffness: 220, damping: 22 }}
          >
            <div style={{ transform: `translateX(${off}px)` }}>
              <span className="flex h-6 w-6 items-center justify-center rounded-full border-2 bg-panel text-[10px] font-bold shadow-[0_2px_6px_rgba(0,0,0,0.5)]" style={{ borderColor: a.color, color: a.color }}>
                {a.name[0]}
              </span>
            </div>
          </motion.div>
        );
      })}
    </div>
  );
}

// AI personas + helpers for the Live Action Stage
const PERSONALITY: Record<string, string> = { A: "Long-Term Planner", B: "Aggressive Investor", C: "Negotiator", D: "Risk Manager" };
const GROUP_NAME: Record<string, string> = {
  "#8d6e63": "Brown",
  "#22b8cf": "Cyan",
  "#ec4899": "Pink",
  "#f59e0b": "Orange",
  "#ef4444": "Red",
  "#eab308": "Yellow",
  "#22c55e": "Green",
  "#6366f1": "Blue",
  "#64748b": "Rail",
  "#14b8a6": "Utility",
};
function netWorthOf(d: Derived, id: string) {
  let n = d.cash[id];
  for (const k of Object.keys(d.owners)) if (d.owners[+k] === id) n += priceOf(+k) + (d.houses[+k] ?? 0) * HOUSE;
  return n;
}
function pseudo(seed: number, min: number, max: number) {
  const x = Math.abs(Math.sin(seed * 12.9898) * 43758.5453) % 1;
  return Math.round(min + x * (max - min));
}
function decisionOf(step: MStep): { icon: string; label: string; accent: string } {
  switch (step.kind) {
    case "buy":
      return { icon: "🏷", label: "Buying Property", accent: "#22c55e" };
    case "rent":
      return { icon: "💸", label: "Paying Rent", accent: "#f59e0b" };
    case "build":
      return { icon: "🏗", label: "Building", accent: "#ec4899" };
    case "trade":
      return step.trade?.status === "accept" ? { icon: "🤝", label: "Trade Accepted", accent: "#22c55e" } : step.trade?.status === "reject" ? { icon: "🚫", label: "Trade Declined", accent: "#ef4444" } : { icon: "🤝", label: "Negotiating Trade", accent: "#6366f1" };
    case "roll":
      return { icon: "🎲", label: "Evaluating Move", accent: "#818cf8" };
    default:
      return { icon: "🧠", label: "Reasoning", accent: "#818cf8" };
  }
}
function synthThought(step: MStep, focus: Space | null, curName: string): string {
  if (step.kind === "buy" && focus) return `Acquiring ${focus.name} strengthens my board position and lifts my rent ceiling.`;
  if (step.kind === "rent") return "Paying rent trims liquidity — keeping enough runway for the next lap.";
  if (step.kind === "build") return "Converting a monopoly into rent pressure before rivals recover.";
  if (step.kind === "roll" && focus) return `Landing near ${focus.name}. Weighing acquisition against liquidity.`;
  return `${curName} is reading the board and rivals before committing capital.`;
}

/* ─── Live Action Stage — the center never sits empty; it narrates the turn ─── */
function CenterStage({ d, cur, step }: { d: Derived; cur: MAgent; step: MStep }) {
  const sum = d.dice ? d.dice[0] + d.dice[1] : 0;
  const rolled = step.kind === "roll";
  const pos = d.positions[cur.id];

  const focusIdx =
    step.kind === "buy" && step.buy != null ? step.buy : rolled && BOARD[pos]?.type === "prop" ? pos : step.kind === "build" && step.build ? step.build.spaces[0] : step.kind === "rent" ? pos : null;
  const focus = focusIdx != null ? BOARD[focusIdx] : null;
  const owner = focus && focusIdx != null ? MAGENTS.find((a) => a.id === d.owners[focusIdx]) : undefined;

  const groupSpaces = focus?.group ? GROUPS[focus.group] ?? [] : [];
  const ownedByCur = groupSpaces.filter((i) => d.owners[i] === cur.id).length;
  const groupPct = groupSpaces.length ? Math.round((ownedByCur / groupSpaces.length) * 100) : 0;
  const roi = focus?.price ? Math.round((rentOf(focusIdx as number, 3) / focus.price) * 100) : pseudo(pos + 1, 12, 22);
  const confidence = pseudo((focusIdx ?? pos) + cur.name.length, 78, 95);

  const decision = decisionOf(step);
  const thought = step.text ?? synthThought(step, focus, cur.name);

  const net = netWorthOf(d, cur.id);
  const liquidity = d.cash[cur.id];
  const risk = liquidity < 300 ? { t: "High", c: "#ef4444" } : liquidity < 700 ? { t: "Medium", c: "#f59e0b" } : { t: "Low", c: "#22c55e" };
  const bestMono = Math.max(
    0,
    ...Object.values(GROUPS).map((sp) => Math.round((sp.filter((i) => d.owners[i] === cur.id).length / sp.length) * 100)),
  );
  const rentFly = step.kind === "rent" ? step.rent : null;

  return (
    <div className="flex w-full max-w-[320px] flex-col items-center gap-2.5 px-2 text-center">
      {/* personality */}
      <span className="rounded-full border border-line bg-panel/70 px-2 py-0.5 font-mono text-[9px] uppercase tracking-wider text-fg-muted">{PERSONALITY[cur.id] ?? "Strategist"}</span>

      {/* current player */}
      <div className="flex items-center gap-3">
        <motion.span
          key={cur.id}
          className="mnx-turn flex h-12 w-12 items-center justify-center rounded-full border-2 bg-panel text-base font-bold shadow"
          style={{ borderColor: cur.color, color: cur.color, ["--mnx-c" as string]: `${cur.color}66` } as React.CSSProperties}
          initial={{ scale: 0.85 }}
          animate={{ scale: 1 }}
          transition={{ type: "spring", stiffness: 260, damping: 20 }}
        >
          {cur.name[0]}
        </motion.span>
        <div className="text-left">
          <p className="text-[15px] font-semibold leading-tight text-fg">{cur.name}</p>
          <p className="font-mono text-[10px] text-fg-muted">{cur.dev}</p>
          <p className="mt-0.5 font-mono text-[11px] font-semibold" style={{ color: cur.color }}>
            💰 {money(liquidity)}
          </p>
        </div>
      </div>

      {/* dice + result */}
      <div className="flex items-center gap-2">
        <div className="flex items-center gap-1.5" key={`dice-${rolled}-${sum}`}>
          <Die n={d.dice?.[0] ?? 1} roll={rolled} />
          <Die n={d.dice?.[1] ?? 1} roll={rolled} delay />
        </div>
        {rolled && sum > 0 && (
          <motion.span initial={{ opacity: 0, x: -4 }} animate={{ opacity: 1, x: 0 }} className="rounded-md border border-line bg-panel-2/50 px-2 py-1 font-mono text-[11px] font-semibold text-fg">
            Rolled {sum}
          </motion.span>
        )}
      </div>

      {/* decision */}
      <div className="flex items-center gap-1.5 rounded-lg border px-2.5 py-1" style={{ borderColor: `${decision.accent}55`, background: `${decision.accent}12`, color: decision.accent }}>
        <span>{decision.icon}</span>
        <span className="text-[12px] font-semibold">{decision.label}</span>
      </div>

      {/* contextual card: property preview OR reasoning, morphing per turn */}
      <div className="relative min-h-[120px] w-full">
        <AnimatePresence mode="wait">
          <motion.div key={`ctx-${step.kind}-${focusIdx ?? "x"}-${thought.slice(0, 8)}`} initial={{ opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -6 }} transition={{ duration: 0.2 }} className="w-full space-y-2">
            {focus && (
              <div className="rounded-xl border border-line bg-panel-2/40 p-3 text-left">
                <div className="flex items-center justify-between">
                  <span className="flex items-center gap-1.5 text-[12px] font-semibold text-fg">
                    <span className="h-2.5 w-2.5 rounded-sm" style={{ background: focus.group }} />
                    {focus.name}
                  </span>
                  <span className="font-mono text-[11px] font-semibold text-fg">{money(focus.price ?? 0)}</span>
                </div>
                <div className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1 font-mono text-[10px] text-fg-muted">
                  <span>Owner</span>
                  <span className="text-right font-semibold" style={{ color: owner?.color ?? "rgb(var(--k-fg))" }}>
                    {owner?.name ?? "Unowned"}
                  </span>
                  <span>Expected ROI</span>
                  <span className="text-right font-semibold text-emerald-400">{roi}%</span>
                  <span>{GROUP_NAME[focus.group ?? ""] ?? "Set"} set</span>
                  <span className="text-right font-semibold text-fg">
                    {ownedByCur}/{groupSpaces.length} · {groupPct}%
                  </span>
                </div>
              </div>
            )}
            {/* AI reasoning */}
            <div className="rounded-xl border border-line bg-panel-2/40 p-3 text-left">
              <p className="flex items-center gap-1 font-mono text-[9px] uppercase tracking-wider text-fg-muted">🧠 Current thought</p>
              <p className="mt-1 line-clamp-3 text-[12px] leading-snug text-fg">{thought}</p>
              <div className="mt-2 flex items-center gap-3 font-mono text-[10px]">
                <span className="text-fg-muted">
                  ROI <span className="font-semibold text-emerald-400">{roi}%</span>
                </span>
                <span className="text-fg-muted">
                  Confidence <span className="font-semibold" style={{ color: cur.color }}>{confidence}%</span>
                </span>
              </div>
            </div>
          </motion.div>
        </AnimatePresence>
        {rentFly && (
          <div className="pointer-events-none absolute -top-3 left-1/2 -translate-x-1/2">
            {[0, 1, 2].map((i) => (
              <span key={i} className="mnx-coin absolute text-[13px]" style={{ left: `${(i - 1) * 12}px`, animationDelay: `${i * 120}ms` }}>
                🪙
              </span>
            ))}
          </div>
        )}
      </div>

      {/* financial insights */}
      <div className="grid w-full grid-cols-4 gap-1.5">
        <Insight k="ROI" v={`${Math.round((net / cur.startCash - 1) * 100)}%`} tone="#22c55e" />
        <Insight k="Cash" v={`$${(liquidity / 1000).toFixed(1)}k`} />
        <Insight k="Risk" v={risk.t} tone={risk.c} />
        <Insight k="Mono" v={`${bestMono}%`} tone="#6366f1" />
      </div>
    </div>
  );
}

function Insight({ k, v, tone }: { k: string; v: React.ReactNode; tone?: string }) {
  return (
    <div className="rounded-lg border border-line bg-panel-2/40 py-1.5">
      <div className="text-[12px] font-semibold" style={{ color: tone ?? "rgb(var(--k-fg))" }}>
        {v}
      </div>
      <div className="font-mono text-[8px] uppercase tracking-wider text-fg-muted">{k}</div>
    </div>
  );
}

const PIPS: Record<number, number[]> = { 1: [4], 2: [0, 8], 3: [0, 4, 8], 4: [0, 2, 6, 8], 5: [0, 2, 4, 6, 8], 6: [0, 2, 3, 5, 6, 8] };
function Die({ n, roll, delay }: { n: number; roll: boolean; delay?: boolean }) {
  return (
    <div className={cn("grid h-9 w-9 grid-cols-3 grid-rows-3 gap-0.5 rounded-lg border border-line bg-gradient-to-b from-panel to-panel-2 p-1 shadow-[0_2px_6px_rgba(0,0,0,0.5)]", roll && "mnx-dice")} style={delay ? { animationDelay: "80ms" } : undefined}>
      {Array.from({ length: 9 }).map((_, i) => (
        <span key={i} className={cn("h-1.5 w-1.5 place-self-center rounded-full", PIPS[n]?.includes(i) ? "bg-fg" : "bg-transparent")} />
      ))}
    </div>
  );
}

function HoverCard({ sp, d }: { sp: Space; d: Derived }) {
  const owner = d.owners[sp.i];
  const ownerAgent = MAGENTS.find((a) => a.id === owner);
  const houses = d.houses[sp.i] ?? 0;
  const rent = rentOf(sp.i, houses);
  const roi = ownerAgent ? Math.round(((d.spaceIncome[sp.i] ?? 0) / (priceOf(sp.i) || 1)) * 100) : 0;
  return (
    <motion.div
      initial={{ opacity: 0, y: 6 }}
      animate={{ opacity: 1, y: 0 }}
      exit={{ opacity: 0, y: 6 }}
      transition={{ duration: 0.16 }}
      className="absolute left-1/2 top-2 z-30 w-56 -translate-x-1/2 rounded-xl border border-line bg-panel/95 p-3 shadow-2xl backdrop-blur"
    >
      <div className="flex items-center gap-2">
        <span className="h-3 w-3 rounded-sm" style={{ background: sp.group }} />
        <span className="text-[13px] font-semibold text-fg">{sp.name}</span>
      </div>
      <div className="mt-2 grid grid-cols-2 gap-x-3 gap-y-1.5 text-[11px]">
        <HoverRow k="Owner" v={ownerAgent?.name ?? "Unowned"} c={ownerAgent?.color} />
        <HoverRow k="Price" v={money(priceOf(sp.i))} />
        <HoverRow k="Rent" v={money(rent)} />
        <HoverRow k="Houses" v={houses >= HOTEL ? "Hotel" : String(houses)} />
        <HoverRow k="ROI" v={`${roi}%`} />
        <HoverRow k="Landed" v={String(d.spaceLanded[sp.i] ?? 0)} />
        <HoverRow k="Income" v={money(d.spaceIncome[sp.i] ?? 0)} />
        <HoverRow k="Status" v={d.mortgaged[sp.i] ? "Mortgaged" : "Active"} />
      </div>
    </motion.div>
  );
}
function HoverRow({ k, v, c }: { k: string; v: string; c?: string }) {
  return (
    <div className="flex items-center justify-between">
      <span className="font-mono text-[9px] uppercase tracking-wider text-fg-muted">{k}</span>
      <span className="font-semibold" style={{ color: c ?? "rgb(var(--k-fg))" }}>
        {v}
      </span>
    </div>
  );
}

/* ─────────────────── live-event toast + trade overlay ─────────────────── */
function LiveEventToast({ idx, text }: { idx: number; text?: string }) {
  if (!text) return null;
  return (
    <AnimatePresence mode="wait">
      <motion.div
        key={idx}
        initial={{ opacity: 0, y: -8 }}
        animate={{ opacity: 1, y: 0 }}
        exit={{ opacity: 0, y: -8 }}
        transition={{ duration: 0.25 }}
        className="absolute left-1/2 top-3 z-20 -translate-x-1/2 rounded-full border border-line bg-panel/90 px-3 py-1.5 text-[11px] font-medium text-fg shadow-lg backdrop-blur"
      >
        <span className="mr-1.5 inline-block h-1.5 w-1.5 rounded-full bg-brand align-middle" /> {text}
      </motion.div>
    </AnimatePresence>
  );
}

function TradeOverlay({ trade, result }: { trade: NonNullable<MStep["trade"]>; result: "accept" | "reject" | null }) {
  const from = MAGENTS.find((a) => a.id === trade.from)!;
  const to = MAGENTS.find((a) => a.id === trade.to)!;
  const badge = result === "accept" ? { t: "Accepted", c: "#22c55e" } : result === "reject" ? { t: "Rejected", c: "#ef4444" } : { t: "Proposal", c: "#6366f1" };
  return (
    <motion.div className="fixed inset-0 z-50 flex items-center justify-center p-4" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}>
      <div className="absolute inset-0 bg-black/70 backdrop-blur-md" />
      <motion.div className="relative w-full max-w-lg rounded-2xl border border-line bg-panel/95 p-6 shadow-2xl backdrop-blur" initial={{ scale: 0.94, y: 14 }} animate={{ scale: 1, y: 0 }} exit={{ scale: 0.96, opacity: 0 }} transition={{ type: "spring", stiffness: 260, damping: 24 }}>
        <div className="text-center">
          <span className="inline-flex items-center gap-1.5 rounded-full border px-3 py-1 font-mono text-[11px] uppercase tracking-wider" style={{ borderColor: `${badge.c}55`, color: badge.c }}>
            Trade {badge.t}
          </span>
        </div>

        <div className="mt-5 flex items-center justify-center gap-4">
          <Trader agent={from} />
          <motion.div animate={{ x: result === "accept" ? [0, 6, 0] : 0 }} transition={{ repeat: result ? 0 : Infinity, duration: 1.4 }}>
            <ArrowRight className="h-6 w-6" style={{ color: badge.c }} />
          </motion.div>
          <Trader agent={to} />
        </div>

        <div className="mt-5 grid grid-cols-2 gap-3">
          <div className="rounded-xl border border-line bg-panel-2/50 p-3">
            <p className="font-mono text-[10px] uppercase tracking-wider text-fg-muted">Offering</p>
            <p className="mt-1 text-[14px] font-semibold text-fg">{trade.give || "—"}</p>
          </div>
          <div className="rounded-xl border border-line bg-panel-2/50 p-3">
            <p className="font-mono text-[10px] uppercase tracking-wider text-fg-muted">Requesting</p>
            <p className="mt-1 text-[14px] font-semibold text-fg">{trade.get || "—"}</p>
          </div>
        </div>

        <div className="mt-5 flex justify-center gap-2">
          {(["Accept", "Reject", "Counter"] as const).map((b) => {
            const active = (b === "Accept" && result === "accept") || (b === "Reject" && result === "reject");
            const c = b === "Accept" ? "#22c55e" : b === "Reject" ? "#ef4444" : "#8d8da1";
            return (
              <span key={b} className={cn("rounded-lg border px-4 py-1.5 text-[12px] font-semibold transition", active ? "text-white" : "text-fg-muted")} style={{ borderColor: `${c}55`, background: active ? c : "transparent", color: active ? "#fff" : undefined }}>
                {active && result === "accept" ? "Accepted" : active && result === "reject" ? "Rejected" : b}
              </span>
            );
          })}
        </div>
      </motion.div>
    </motion.div>
  );
}
function Trader({ agent }: { agent: MAgent }) {
  return (
    <div className="flex flex-col items-center gap-1">
      <span className="flex h-12 w-12 items-center justify-center rounded-full border-2 text-base font-bold" style={{ borderColor: agent.color, color: agent.color }}>
        {agent.name[0]}
      </span>
      <span className="text-[12px] font-semibold text-fg">{agent.name}</span>
      <span className="font-mono text-[9px] text-fg-muted">{agent.dev}</span>
    </div>
  );
}

/* ─────────────────── net-worth multi-line graph ─────────────────── */
function NetWorthGraph({ data }: { data: NetPoint[] }) {
  return (
    <ResponsiveContainer width="100%" height="100%">
      <LineChart data={data} margin={{ top: 6, right: 8, bottom: 0, left: -18 }}>
        <CartesianGrid strokeDasharray="3 6" stroke="rgb(var(--k-line))" strokeOpacity={0.5} vertical={false} />
        <XAxis dataKey="label" tick={{ fill: "rgb(var(--k-fg-muted))", fontSize: 10 }} axisLine={false} tickLine={false} />
        <YAxis tick={{ fill: "rgb(var(--k-fg-muted))", fontSize: 10 }} axisLine={false} tickLine={false} tickFormatter={(v) => `$${(v / 1000).toFixed(1)}k`} width={44} />
        <Tooltip
          contentStyle={{ background: "rgb(var(--k-panel))", border: "1px solid rgb(var(--k-line))", borderRadius: 10, fontSize: 12 }}
          labelStyle={{ color: "rgb(var(--k-fg-muted))" }}
          formatter={(v: number, name: string) => [money(v), MAGENTS.find((a) => a.id === name)?.name ?? name]}
        />
        {MAGENTS.map((a) => (
          <Line key={a.id} type="monotone" dataKey={a.id} stroke={a.color} strokeWidth={2} dot={false} isAnimationActive animationDuration={300} />
        ))}
      </LineChart>
    </ResponsiveContainer>
  );
}

/* ─────────────────── portfolio card ─────────────────── */
function PortfolioCard({ agent, rank, cash, net, props, houses, chat, onClick }: { agent: MAgent; rank: number; cash: number; net: number; props: number[]; houses: Record<number, number>; chat: ChatMsg[]; onClick: () => void }) {
  const hotels = props.filter((p) => (houses[p] ?? 0) >= HOTEL).length;
  const houseCount = props.reduce((s, p) => s + Math.min(HOTEL - 1, houses[p] ?? 0), 0);
  const roi = Math.round((net / agent.startCash - 1) * 100);
  const last = [...chat].reverse().find((c) => c.agent.id === agent.id);
  const status = last?.step.intent ? MINTENT[last.step.intent] : null;
  return (
    <button onClick={onClick} className="flex w-full items-center gap-3 rounded-xl border border-line bg-gradient-to-b from-panel-2/50 to-panel/40 px-3 py-2.5 text-left transition hover:border-brand/40">
      <div className="relative">
        <span className="flex h-9 w-9 items-center justify-center rounded-full border-2 text-[12px] font-bold" style={{ borderColor: agent.color, color: agent.color }}>
          {agent.name[0]}
        </span>
        {rank === 0 && <Crown className="absolute -right-1.5 -top-2 h-3.5 w-3.5 text-amber-400" fill="currentColor" />}
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center justify-between">
          <span className="text-[13px] font-semibold text-fg">
            <span className="mr-1 font-mono text-[10px] text-fg-muted">#{rank + 1}</span>
            {agent.name}
          </span>
          <span className="font-mono text-[12px] font-semibold text-fg">{money(net)}</span>
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-x-2.5 gap-y-0.5 font-mono text-[10px] text-fg-muted">
          <span>💰 {money(cash)}</span>
          <span>🏠 {props.length}</span>
          {hotels > 0 && <span>🏨 {hotels}</span>}
          <span className={roi >= 0 ? "text-emerald-400" : "text-red-400"}>📈 {roi >= 0 ? "+" : ""}{roi}%</span>
          {status && (
            <span className="inline-flex items-center gap-1" style={{ color: status.color }}>
              {status.icon} {status.label}
            </span>
          )}
        </div>
      </div>
    </button>
  );
}

/* ───────────────────────────── standings ─────────────────────────────── */
function Standings({
  ranked,
  netWorth,
  cash,
  propsOf,
  houses,
  netHistory,
  onInspect,
}: {
  ranked: MAgent[];
  netWorth: (id: string) => number;
  cash: Record<string, number>;
  propsOf: (id: string) => number[];
  houses: Record<number, number>;
  netHistory: { label: string; value: number }[];
  onInspect: (id: string) => void;
}) {
  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
      <Panel className="p-4 lg:col-span-2">
        <PanelTitle icon={ListOrdered}>Standings</PanelTitle>
        <div className="mt-3 overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">
                <th className="pb-2 text-left font-medium">#</th>
                <th className="pb-2 text-left font-medium">Agent</th>
                <th className="pb-2 text-right font-medium">Net worth</th>
                <th className="pb-2 text-right font-medium">Cash</th>
                <th className="pb-2 text-right font-medium">Props</th>
                <th className="pb-2 text-right font-medium">Houses</th>
              </tr>
            </thead>
            <tbody className="text-fg">
              {ranked.map((a, i) => {
                const ps = propsOf(a.id);
                const h = ps.reduce((s, p) => s + (houses[p] ?? 0), 0);
                return (
                  <tr key={a.id} onClick={() => onInspect(a.id)} className="cursor-pointer border-t border-line hover:bg-panel-2/40">
                    <td className="py-2 font-mono text-fg-muted">{i + 1}</td>
                    <td className="py-2">
                      <span className="flex items-center gap-2 font-medium">
                        <span className="h-2.5 w-2.5 rounded-full" style={{ background: a.color }} />
                        {a.name}
                        <span className="font-mono text-[10px] text-fg-muted">{a.dev}</span>
                      </span>
                    </td>
                    <td className="py-2 text-right font-mono font-semibold text-fg">{money(netWorth(a.id))}</td>
                    <td className="py-2 text-right font-mono text-fg-muted">{money(cash[a.id])}</td>
                    <td className="py-2 text-right font-mono">{ps.length}</td>
                    <td className="py-2 text-right font-mono">{h}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </Panel>
      <Panel className="p-4">
        <PanelTitle icon={TrendingUp}>Total Net Worth</PanelTitle>
        <div className="mt-3">
          <AreaTrend data={netHistory} dataKey="value" xKey="label" color={CHART.brand} id="monoNet" height={220} valuePrefix="$" />
        </div>
      </Panel>
    </div>
  );
}

/* ───────────────────────────── shared bits ─────────────────────────────── */
function Panel({ className, children }: { className?: string; children: React.ReactNode }) {
  return (
    <section className={cn("min-w-0 rounded-xl border border-line bg-panel/80 shadow-[0_1px_2px_rgba(15,23,42,0.04),0_12px_30px_-18px_rgba(15,23,42,0.25)] backdrop-blur", className)}>{children}</section>
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
function Metric({ icon: Icon, label, value, tone }: { icon: React.ElementType; label: string; value: React.ReactNode; tone: string }) {
  return (
    <div className="flex items-center gap-1.5">
      <Icon className={cn("h-4 w-4", tone)} />
      <span className="max-w-[90px] truncate text-sm font-semibold text-fg">{value}</span>
      <span className="hidden font-mono text-[10px] uppercase tracking-wider text-fg-muted sm:inline">{label}</span>
    </div>
  );
}
function SubTabs({ tab, onSelect }: { tab: Tab; onSelect: (t: Tab) => void }) {
  const items: { key: Tab; label: string }[] = [
    { key: "live", label: "Live Match" },
    { key: "standings", label: "Standings" },
    { key: "replays", label: "Replays" },
  ];
  return (
    <div className="flex items-center gap-1 border-b border-line">
      {items.map((it) => (
        <button key={it.key} onClick={() => onSelect(it.key)} className={cn("relative px-3 py-2 text-xs font-medium transition-colors", tab === it.key ? "text-fg" : "text-fg-muted hover:text-fg")}>
          {it.label}
          {tab === it.key && <span className="absolute inset-x-2 -bottom-px h-0.5 rounded-full bg-brand" />}
        </button>
      ))}
    </div>
  );
}

function Inspector({ agent, d, netWorth, props, monopolies, onClose }: { agent: MAgent; d: Derived; netWorth: number; props: number[]; monopolies: Set<number>; onClose: () => void }) {
  const houses = props.reduce((s, p) => s + (d.houses[p] ?? 0), 0);
  const income = props.reduce((s, p) => s + (d.spaceIncome[p] ?? 0), 0);
  const myMonoGroups = new Set(props.filter((p) => monopolies.has(p)).map((p) => BOARD[p].group));
  const roi = Math.round((netWorth / agent.startCash - 1) * 100);
  return (
    <div className="fixed inset-0 z-50 flex justify-end" role="dialog" aria-modal="true">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      <motion.div className="relative h-full w-full max-w-sm overflow-y-auto border-l border-line bg-panel p-5 shadow-2xl" initial={{ x: 40, opacity: 0 }} animate={{ x: 0, opacity: 1 }} transition={{ type: "spring", stiffness: 300, damping: 30 }}>
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
          <Field k="Net worth" v={money(netWorth)} />
          <Field k="Cash" v={money(d.cash[agent.id])} />
          <Field k="Properties" v={props.length} />
          <Field k="Houses" v={houses} />
          <Field k="Monopolies" v={myMonoGroups.size} />
          <Field k="ROI" v={`${roi >= 0 ? "+" : ""}${roi}%`} />
          <Field k="Income" v={money(income)} />
          <Field k="Position" v={BOARD[d.positions[agent.id]].name} />
        </div>
        <div className="mt-5">
          <p className="font-mono text-[10px] uppercase tracking-wider text-fg-muted">Portfolio</p>
          <div className="mt-2 space-y-1.5">
            {props.map((p) => (
              <div key={p} className="flex items-center justify-between rounded-md border border-line bg-panel-2/40 px-2.5 py-1.5">
                <span className="flex items-center gap-2 text-[12px] text-fg">
                  <span className="h-2.5 w-2.5 rounded-sm" style={{ background: BOARD[p].group ?? "#cbd5e1" }} />
                  {BOARD[p].name}
                  {monopolies.has(p) && <span className="rounded bg-emerald-500/15 px-1 font-mono text-[9px] text-emerald-400">SET</span>}
                </span>
                <span className="font-mono text-[11px] text-fg-muted">
                  ${BOARD[p].price}
                  {(d.houses[p] ?? 0) >= HOTEL ? " · 🏨" : (d.houses[p] ?? 0) > 0 ? ` · ${d.houses[p]}🏠` : ""}
                </span>
              </div>
            ))}
            {props.length === 0 && <p className="font-mono text-[11px] text-fg-muted">No properties owned</p>}
          </div>
        </div>
      </motion.div>
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
