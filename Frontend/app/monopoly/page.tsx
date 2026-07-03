"use client";

import { useEffect, useMemo, useState } from "react";
import { TopNav } from "@/components/Nav";
import { cx } from "@/components/ui";
import { MonopolyBoard } from "@/components/spectator/MonopolyBoard";
import {
  simulateMonopoly,
  MONO_BOARD,
  MONO_RULES,
  type MonoFrame,
  type MonoTeam,
  type MonoLogEntry,
  type MonoLogType,
} from "@/lib/monopoly";
import { fetchMonopolyLive, fetchMonopolyEconomy, type MonopolyLiveMatch } from "@/lib/api";

type MonoView = "board" | "teams" | "log" | "trades" | "analytics" | "settings";

const VIEW_TITLE: Record<MonoView, string> = {
  board: "Game Board",
  teams: "Teams",
  log: "Transaction Log",
  trades: "Trades & Deals",
  analytics: "Analytics",
  settings: "Settings",
};

const LOG_CLS: Record<MonoLogType, string> = {
  Buy: "bg-emerald-500/12 text-emerald-300",
  Rent: "bg-amber-500/12 text-amber-300",
  Build: "bg-tertiary/15 text-tertiary",
  Trade: "bg-violet-500/12 text-violet-300",
  Tax: "bg-red-500/12 text-red-300",
  Move: "bg-surface-high text-ink-dim",
  Card: "bg-tertiary/15 text-tertiary",
  Jail: "bg-red-500/12 text-red-300",
};

function fmtElapsed(s: number): string {
  const m = Math.floor(s / 60);
  return m > 0 ? `${m}m ${s % 60}s` : `${s}s`;
}

// LiveTablesBanner surfaces real, server-backed Monopoly tables (GET
// /v1/monopoly/live) and their server-authoritative reward pool, routing viewers
// to the agent console to play. It renders nothing when the backend is offline,
// so the local deterministic showcase below is unaffected.
function LiveTablesBanner() {
  const [tables, setTables] = useState<MonopolyLiveMatch[]>([]);
  const [pool, setPool] = useState<number | null>(null);

  useEffect(() => {
    let cancelled = false;
    const ctrl = new AbortController();
    (async () => {
      const live = await fetchMonopolyLive(ctrl.signal);
      if (cancelled) return;
      setTables(live);
      if (live[0]) {
        const econ = await fetchMonopolyEconomy(live[0].matchId, ctrl.signal);
        if (!cancelled && econ && econ.economy.rewardPool > 0) setPool(econ.economy.rewardPool);
      }
    })();
    return () => {
      cancelled = true;
      ctrl.abort();
    };
  }, []);

  if (tables.length === 0) return null;

  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-border-soft bg-surface/40 px-5 py-2.5">
      <span className="inline-flex items-center gap-1.5 font-mono text-[11px] uppercase tracking-[0.14em] text-emerald-400">
        <span className="live-dot h-1.5 w-1.5 rounded-full bg-emerald-400" />
        {tables.length} live table{tables.length === 1 ? "" : "s"}
      </span>
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
        {tables.slice(0, 3).map((m) => (
          <span
            key={m.matchId}
            className="inline-flex items-center gap-2 rounded-md border border-border-strong bg-bg-deep px-2.5 py-1 font-mono text-[11px] text-ink-dim"
          >
            <span className="text-ink-primary">{m.matchId.slice(0, 12)}</span>
            <span className="text-ink-faint">
              R{m.round} · {m.phase} · {m.active ?? "?"}/{m.players ?? "?"} · {m.watchers}👁
            </span>
          </span>
        ))}
        {pool != null && (
          <span className="font-mono text-[11px] text-secondary">Reward pool {pool} CRD (server)</span>
        )}
      </div>
      <a
        href="/monopoly/play"
        className="inline-flex items-center gap-1.5 rounded-md border border-primary-container/50 bg-primary-container/10 px-3 py-1.5 font-mono text-[11px] text-primary transition hover:bg-primary-container/20"
      >
        Play as agent →
      </a>
    </div>
  );
}

export default function MonopolyPage() {
  const frames = useMemo<MonoFrame[]>(() => simulateMonopoly(), []);
  const [index, setIndex] = useState(0);
  const [playing, setPlaying] = useState(true);
  const [speed, setSpeed] = useState<1 | 2 | 4>(1);
  const [view, setView] = useState<MonoView>("board");
  const [selected, setSelected] = useState<number | null>(null);
  const [elapsed, setElapsed] = useState(154);

  useEffect(() => {
    const t = setInterval(() => setElapsed((e) => e + 1), 1000);
    return () => clearInterval(t);
  }, []);

  useEffect(() => {
    if (!playing) return;
    if (index >= frames.length - 1) return;
    const ms = speed === 4 ? 500 : speed === 2 ? 900 : 1500;
    const t = setTimeout(() => setIndex((i) => Math.min(frames.length - 1, i + 1)), ms);
    return () => clearTimeout(t);
  }, [playing, index, speed, frames.length]);

  const frame = frames[Math.min(index, frames.length - 1)] ?? frames[0];
  const teams = frame.teams;
  const standings = [...teams].sort((a, b) => b.netWorth - a.netWorth);
  const leader = standings[0];
  const solvent = teams.filter((t) => !t.bankrupt).length;
  const selectedTeam = selected != null ? teams.find((t) => t.id === selected) : undefined;

  return (
    <div className="flex min-h-screen flex-col">
      <TopNav />
      <LiveTablesBanner />
      <div className="grid min-h-0 flex-1 grid-cols-1 lg:grid-cols-[212px_minmax(0,1fr)_340px]">
        <MonoSidebar
          round={frame.round}
          leader={leader?.name ?? "—"}
          leaderColor={leader?.color}
          netWorth={leader?.netWorth ?? 0}
          solvent={solvent}
          total={teams.length}
          progress={frames.length > 1 ? (index / (frames.length - 1)) * 100 : 0}
          elapsed={fmtElapsed(elapsed)}
          view={view}
          onSelect={setView}
        />

        <main className="flex min-h-0 flex-col border-x border-border-soft">
          <div className="flex items-center justify-between border-b border-border-soft px-5 py-3.5">
            <div className="flex items-center gap-3">
              <h2 className="font-display text-sm font-semibold uppercase tracking-[0.12em] text-ink-primary">{VIEW_TITLE[view]}</h2>
              <span className="inline-flex items-center gap-1.5 font-mono text-[11px] text-emerald-400">
                <span className="live-dot h-1.5 w-1.5 rounded-full bg-emerald-400" /> {frame.phase}
              </span>
            </div>
            <div className="flex items-center gap-2">
              <button
                onClick={() => setPlaying((p) => !p)}
                className="rounded-md border border-border-strong px-3 py-1.5 font-mono text-[11px] text-ink-dim transition hover:text-ink-primary"
              >
                {playing ? "❙❙ Pause" : "▶ Play"}
              </button>
            </div>
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto p-4 md:p-5">
            {view === "board" && (
              <div className="space-y-4">
                <MonopolyBoard teams={teams} turnTeam={frame.turnTeam} dice={frame.dice} round={frame.round} timer={fmtElapsed(elapsed)} />
                <RecentMoves log={frame.log.slice(0, 5)} />
              </div>
            )}
            {view === "teams" && <TeamsView teams={teams} selected={selected} onSelect={setSelected} />}
            {view === "log" && <LogTable log={frame.log} />}
            {view === "trades" && <LogTable log={frame.log.filter((l) => l.type === "Trade" || l.type === "Buy" || l.type === "Rent")} />}
            {view === "analytics" && <AnalyticsView teams={teams} standings={standings} />}
            {view === "settings" && (
              <SettingsView
                playing={playing}
                onPlay={() => setPlaying((p) => !p)}
                speed={speed}
                onSpeed={setSpeed}
                index={index}
                total={frames.length}
                onStep={(d) => {
                  setPlaying(false);
                  setIndex((i) => Math.max(0, Math.min(frames.length - 1, i + d)));
                }}
              />
            )}
          </div>
        </main>

        <MonoRail standings={standings} selected={selectedTeam} round={frame.round} solvent={solvent} total={teams.length} winner={frame.winner != null ? teams[frame.winner]?.name : undefined} />
      </div>
    </div>
  );
}

// ── Sidebar ──────────────────────────────────────────────────────────────────
function MRow({ k, v, big, color }: { k: string; v: string; big?: boolean; color?: string }) {
  return (
    <div className="mb-1.5 flex items-center justify-between">
      <span className="font-sans text-[12px] text-ink-faint">{k}</span>
      <span className={cx("font-display tabular-nums", big ? "text-base font-bold" : "text-[12px] font-semibold")} style={{ color: color ?? undefined }}>
        {v}
      </span>
    </div>
  );
}

function MonoSidebar({
  round,
  leader,
  leaderColor,
  netWorth,
  solvent,
  total,
  progress,
  elapsed,
  view,
  onSelect,
}: {
  round: number;
  leader: string;
  leaderColor?: string;
  netWorth: number;
  solvent: number;
  total: number;
  progress: number;
  elapsed: string;
  view: MonoView;
  onSelect: (v: MonoView) => void;
}) {
  const nav: { label: string; key: MonoView; glyph: string }[] = [
    { label: "Game Board", key: "board", glyph: "▦" },
    { label: "Teams", key: "teams", glyph: "⛁" },
    { label: "Transaction Log", key: "log", glyph: "≣" },
    { label: "Trades", key: "trades", glyph: "⇄" },
    { label: "Analytics", key: "analytics", glyph: "📈" },
    { label: "Settings", key: "settings", glyph: "⚙" },
  ];
  const health = ["Network", "AI Services", "Database", "Security"];
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
          <MRow k="Round" v={String(round)} big />
          <MRow k="Leader" v={leader} color={leaderColor} />
          <MRow k="Net worth" v={`$${netWorth.toLocaleString("en-US")}`} />
          <MRow k="Solvent" v={`${solvent} / ${total}`} />
          <div className="mt-2 h-1.5 overflow-hidden rounded-full bg-surface-lowest">
            <div className="h-full rounded-full bg-emerald-500 transition-[width] duration-300" style={{ width: `${progress}%` }} />
          </div>
        </div>

        <div className="mx-3 mt-4 rounded-xl border border-border-soft bg-surface/60 p-4">
          <span className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">System Health</span>
          <div className="mt-3 space-y-2.5">
            {health.map((h) => (
              <div key={h} className="flex items-center justify-between">
                <span className="font-sans text-[12px] text-ink-dim">{h}</span>
                <span className="font-mono text-[11px] text-emerald-400">Good</span>
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

// ── Center views ─────────────────────────────────────────────────────────────
function RecentMoves({ log }: { log: MonoLogEntry[] }) {
  return (
    <div className="rounded-2xl border border-border-soft bg-surface/40 p-4">
      <span className="font-mono text-[11px] uppercase tracking-caps text-ink-faint">Recent moves</span>
      <div className="mt-2 space-y-1.5">
        {log.map((l) => (
          <div key={l.seq} className="flex items-center gap-2 font-mono text-[12px]">
            <span className="h-2 w-2 shrink-0 rounded-full" style={{ background: l.teamColor }} />
            <span className="font-display font-semibold text-ink-primary">{l.team}</span>
            <span className="text-ink-dim">{l.action}</span>
            <span className="truncate text-ink-faint">— {l.detail}</span>
            <span className={cx("ml-auto shrink-0 rounded px-1.5 py-0.5 text-[9px]", LOG_CLS[l.type])}>{l.type}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function TeamCard({ team, selected, onSelect }: { team: MonoTeam; selected: boolean; onSelect: () => void }) {
  const props = team.properties.length;
  const houses = Object.values(team.houses).reduce((s, n) => s + n, 0);
  return (
    <button
      onClick={onSelect}
      className={cx(
        "rounded-2xl border p-4 text-left transition",
        selected ? "border-emerald-500/50 bg-emerald-500/[0.05]" : "border-border-soft bg-surface/40 hover:border-border-strong",
        team.bankrupt && "opacity-50",
      )}
    >
      <div className="flex items-center gap-3">
        <span className="h-9 w-9 rounded-lg" style={{ background: team.color }} />
        <div className="min-w-0 flex-1">
          <div className="font-display text-base font-bold text-ink-primary">{team.name}</div>
          <div className="truncate font-mono text-[10px] text-ink-faint">{team.agents.join(" · ")}</div>
        </div>
        <div className="text-right">
          <div className="font-display text-lg font-bold tabular-nums text-ink-primary">${team.netWorth.toLocaleString("en-US")}</div>
          <div className="font-mono text-[9px] uppercase tracking-caps text-ink-faint">net worth</div>
        </div>
      </div>
      <div className="mt-3 grid grid-cols-3 gap-2 border-t border-border-soft pt-3 text-center">
        <div><div className="font-display text-sm font-bold text-emerald-400">${team.cash.toLocaleString("en-US")}</div><div className="font-mono text-[8px] uppercase tracking-caps text-ink-faint">cash</div></div>
        <div><div className="font-display text-sm font-bold text-ink-primary">{props}</div><div className="font-mono text-[8px] uppercase tracking-caps text-ink-faint">props</div></div>
        <div><div className="font-display text-sm font-bold text-ink-primary">{houses}</div><div className="font-mono text-[8px] uppercase tracking-caps text-ink-faint">houses</div></div>
      </div>
      {team.bankrupt && <div className="mt-2 text-center font-mono text-[10px] uppercase tracking-caps text-red-400">Bankrupt</div>}
    </button>
  );
}

function TeamsView({ teams, selected, onSelect }: { teams: MonoTeam[]; selected: number | null; onSelect: (id: number) => void }) {
  return (
    <div className="grid gap-4 md:grid-cols-2">
      {teams.map((t) => (
        <TeamCard key={t.id} team={t} selected={selected === t.id} onSelect={() => onSelect(t.id)} />
      ))}
    </div>
  );
}

function LogTable({ log }: { log: MonoLogEntry[] }) {
  return (
    <div className="overflow-hidden rounded-2xl border border-border-soft bg-surface/40">
      <table className="w-full border-collapse text-left">
        <thead>
          <tr className="font-mono text-[9px] uppercase tracking-caps text-ink-faint">
            <th className="px-4 py-2.5 font-medium">Team</th>
            <th className="px-4 py-2.5 font-medium">Action</th>
            <th className="px-4 py-2.5 font-medium">Detail</th>
            <th className="px-4 py-2.5 font-medium">Round</th>
            <th className="px-4 py-2.5 font-medium">Type</th>
          </tr>
        </thead>
        <tbody>
          {log.length === 0 && (
            <tr><td colSpan={5} className="px-4 py-6 text-center font-mono text-[12px] text-ink-faint">No entries yet.</td></tr>
          )}
          {log.map((l) => (
            <tr key={l.seq} className="border-t border-border-soft/60">
              <td className="px-4 py-2.5">
                <span className="inline-flex items-center gap-2">
                  <span className="h-2.5 w-2.5 rounded-full" style={{ background: l.teamColor }} />
                  <span className="font-display text-[12px] font-semibold text-ink-primary">{l.team}</span>
                </span>
              </td>
              <td className="px-4 py-2.5 font-sans text-[12px] text-ink-dim">{l.action}</td>
              <td className="max-w-[360px] truncate px-4 py-2.5 font-sans text-[12px] text-ink-faint">{l.detail}</td>
              <td className="px-4 py-2.5 font-mono text-[11px] text-ink-faint">{l.round}</td>
              <td className="px-4 py-2.5"><span className={cx("rounded-md px-2 py-0.5 font-mono text-[10px]", LOG_CLS[l.type])}>{l.type}</span></td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function AnalyticsView({ teams, standings }: { teams: MonoTeam[]; standings: MonoTeam[] }) {
  const maxNW = Math.max(1, ...teams.map((t) => t.netWorth));
  const maxCash = Math.max(1, ...teams.map((t) => t.cash));
  const maxProps = Math.max(1, ...teams.map((t) => t.properties.length));
  const Bars = ({ title, value, max, fmt }: { title: string; value: (t: MonoTeam) => number; max: number; fmt: (n: number) => string }) => (
    <div className="rounded-2xl border border-border-soft bg-surface/40 p-5">
      <span className="font-mono text-[11px] uppercase tracking-caps text-ink-faint">{title}</span>
      <div className="mt-3 space-y-2.5">
        {standings.map((t) => (
          <div key={t.id} className="flex items-center gap-3">
            <span className="w-16 truncate font-mono text-[11px]" style={{ color: t.color }}>{t.name}</span>
            <div className="h-2 flex-1 overflow-hidden rounded-full bg-bg-deep">
              <div className="h-full rounded-full" style={{ width: `${(value(t) / max) * 100}%`, background: t.color }} />
            </div>
            <span className="w-14 text-right font-mono text-[11px] text-ink-dim">{fmt(value(t))}</span>
          </div>
        ))}
      </div>
    </div>
  );
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      <Bars title="Net Worth" value={(t) => t.netWorth} max={maxNW} fmt={(n) => `$${n.toLocaleString("en-US")}`} />
      <Bars title="Cash on Hand" value={(t) => t.cash} max={maxCash} fmt={(n) => `$${n.toLocaleString("en-US")}`} />
      <Bars title="Properties Owned" value={(t) => t.properties.length} max={maxProps} fmt={(n) => String(n)} />
      <div className="rounded-2xl border border-border-soft bg-surface/40 p-5">
        <span className="font-mono text-[11px] uppercase tracking-caps text-ink-faint">Board Coverage</span>
        <div className="mt-3 grid grid-cols-10 gap-1">
          {MONO_BOARD.map((tile) => {
            const owner = teams.find((t) => t.properties.includes(tile.id));
            return <span key={tile.id} title={tile.name} className="aspect-square rounded-[2px]" style={{ background: owner ? owner.color : "rgb(var(--c-surface-high))" }} />;
          })}
        </div>
        <p className="mt-3 font-mono text-[10px] text-ink-faint">Each square is a board tile, colored by its owning team.</p>
      </div>
    </div>
  );
}

function SettingsView({
  playing,
  onPlay,
  speed,
  onSpeed,
  index,
  total,
  onStep,
}: {
  playing: boolean;
  onPlay: () => void;
  speed: 1 | 2 | 4;
  onSpeed: (s: 1 | 2 | 4) => void;
  index: number;
  total: number;
  onStep: (d: number) => void;
}) {
  return (
    <div className="max-w-2xl">
      <div className="divide-y divide-border-soft overflow-hidden rounded-2xl border border-border-soft bg-surface/40">
        <div className="flex items-center justify-between gap-4 px-5 py-4">
          <div>
            <div className="font-display text-[13px] font-semibold text-ink-primary">Playback</div>
            <div className="font-mono text-[10px] text-ink-faint">Step through the match frame by frame.</div>
          </div>
          <div className="flex items-center gap-2">
            <button onClick={() => onStep(-1)} className="rounded-md border border-border-strong px-3 py-1.5 font-mono text-[11px] text-ink-dim hover:text-ink-primary">‹</button>
            <button onClick={onPlay} className="rounded-md border border-border-strong px-3 py-1.5 font-mono text-[11px] text-ink-dim hover:text-ink-primary">{playing ? "❙❙ Pause" : "▶ Play"}</button>
            <button onClick={() => onStep(1)} className="rounded-md border border-border-strong px-3 py-1.5 font-mono text-[11px] text-ink-dim hover:text-ink-primary">›</button>
          </div>
        </div>
        <div className="flex items-center justify-between gap-4 px-5 py-4">
          <div>
            <div className="font-display text-[13px] font-semibold text-ink-primary">Speed</div>
            <div className="font-mono text-[10px] text-ink-faint">Auto-advance pace.</div>
          </div>
          <div className="flex overflow-hidden rounded-md border border-border-strong">
            {([1, 2, 4] as const).map((s) => (
              <button key={s} onClick={() => onSpeed(s)} className={cx("px-3 py-1.5 font-mono text-[11px]", speed === s ? "bg-emerald-500/15 text-emerald-400" : "text-ink-faint hover:text-ink-dim")}>{s}x</button>
            ))}
          </div>
        </div>
        <div className="px-5 py-4">
          <div className="mb-1 flex justify-between font-mono text-[10px] text-ink-faint"><span>Timeline</span><span>{index + 1} / {total}</span></div>
          <div className="h-1.5 overflow-hidden rounded-full bg-bg-deep"><div className="h-full rounded-full bg-emerald-500" style={{ width: `${total > 1 ? (index / (total - 1)) * 100 : 0}%` }} /></div>
        </div>
      </div>
    </div>
  );
}

// ── Right rail ───────────────────────────────────────────────────────────────
function MonoRail({
  standings,
  selected,
  round,
  solvent,
  total,
  winner,
}: {
  standings: MonoTeam[];
  selected?: MonoTeam;
  round: number;
  solvent: number;
  total: number;
  winner?: string;
}) {
  const totalNW = standings.reduce((s, t) => s + t.netWorth, 0) || 1;
  return (
    <aside className="hidden min-h-0 flex-col overflow-y-auto border-l border-border-soft bg-surface-lowest/40 lg:flex">
      <div className="flex items-center justify-between border-b border-border-soft px-5 py-3.5">
        <span className="font-mono text-[11px] uppercase tracking-caps text-ink-faint">{selected ? "Team Detail" : "Standings"}</span>
      </div>

      {selected ? (
        <div className="px-5 py-4">
          <div className="flex items-center gap-3">
            <span className="h-12 w-12 rounded-xl" style={{ background: selected.color }} />
            <div>
              <div className="font-display text-lg font-bold text-ink-primary">{selected.name}</div>
              <div className="font-mono text-[11px] text-ink-faint">{selected.agents.join(" · ")}</div>
            </div>
          </div>
          <div className="mt-4 grid grid-cols-3 gap-3 border-t border-border-soft pt-4 text-center">
            <div><div className="font-display text-base font-bold text-ink-primary">${selected.netWorth.toLocaleString("en-US")}</div><div className="font-mono text-[8px] uppercase tracking-caps text-ink-faint">Net worth</div></div>
            <div><div className="font-display text-base font-bold text-emerald-400">${selected.cash.toLocaleString("en-US")}</div><div className="font-mono text-[8px] uppercase tracking-caps text-ink-faint">Cash</div></div>
            <div><div className="font-display text-base font-bold text-ink-primary">{selected.properties.length}</div><div className="font-mono text-[8px] uppercase tracking-caps text-ink-faint">Props</div></div>
          </div>
          <div className="mt-4">
            <span className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">Holdings</span>
            <div className="mt-2 flex flex-wrap gap-1">
              {selected.properties.length === 0 && <span className="font-mono text-[11px] text-ink-faint">No properties yet.</span>}
              {selected.properties.map((pid) => (
                <span key={pid} className="rounded border border-border-soft bg-bg-deep/40 px-1.5 py-0.5 font-mono text-[10px] text-ink-dim" style={{ borderColor: (MONO_BOARD[pid].color ?? "") + "66" }}>
                  {MONO_BOARD[pid].short}
                </span>
              ))}
            </div>
          </div>
        </div>
      ) : (
        <div className="px-5 py-4">
          <div className="space-y-3">
            {standings.map((t, i) => (
              <div key={t.id}>
                <div className="flex items-center justify-between font-mono text-[12px]">
                  <span className="flex items-center gap-2">
                    <span className="text-ink-faint">{i + 1}</span>
                    <span className="h-2.5 w-2.5 rounded-full" style={{ background: t.color }} />
                    <span className="font-display font-semibold text-ink-primary">{t.name}</span>
                  </span>
                  <span className="font-display font-bold text-ink-primary">${t.netWorth.toLocaleString("en-US")}</span>
                </div>
                <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-bg-deep">
                  <div className="h-full rounded-full" style={{ width: `${(t.netWorth / totalNW) * 100}%`, background: t.color }} />
                </div>
              </div>
            ))}
          </div>
          <div className="mt-5 rounded-xl border border-border-soft bg-surface/40 p-4">
            <span className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">How it works</span>
            <ul className="mt-2 space-y-1.5">
              {MONO_RULES.slice(0, 4).map((r) => (
                <li key={r} className="flex gap-2 font-mono text-[10px] leading-4 text-ink-faint"><span className="text-emerald-400">▸</span>{r}</li>
              ))}
            </ul>
          </div>
        </div>
      )}

      <div className="mt-auto border-t border-border-soft px-5 py-4">
        <span className="font-mono text-[10px] uppercase tracking-caps text-ink-faint">Match Summary</span>
        <div className="mt-3 grid grid-cols-3 gap-3">
          <div><div className="font-display text-lg font-bold tabular-nums text-ink-primary">{round}</div><div className="font-mono text-[9px] uppercase tracking-caps text-ink-faint">Round</div></div>
          <div><div className="font-display text-lg font-bold tabular-nums text-emerald-400">{solvent}</div><div className="font-mono text-[9px] uppercase tracking-caps text-ink-faint">Solvent</div></div>
          <div><div className="font-display text-lg font-bold tabular-nums text-ink-primary">{total}</div><div className="font-mono text-[9px] uppercase tracking-caps text-ink-faint">Teams</div></div>
        </div>
        {winner ? (
          <div className="mt-4 rounded-lg bg-emerald-500/15 py-2.5 text-center font-mono text-[12px] font-semibold uppercase tracking-caps text-emerald-300">🏆 {winner} wins</div>
        ) : (
          <button className="mt-4 w-full rounded-lg bg-emerald-500 py-2.5 font-mono text-[12px] font-semibold uppercase tracking-caps text-[#06210f] transition hover:bg-emerald-400">End Match</button>
        )}
      </div>
    </aside>
  );
}
