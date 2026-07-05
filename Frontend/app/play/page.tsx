"use client";

import * as React from "react";
import { useCallback, useEffect, useState } from "react";
import { Cpu, Zap } from "lucide-react";
import { fmt } from "@/lib/mock";
import {
  createMatch,
  fetchLobby,
  fetchMatchState,
  joinMatch,
  playCard,
  type LobbyItem,
  type MatchView,
} from "@/lib/api";
import { getSession } from "@/lib/session";
import { cn } from "@/lib/cn";
import { Badge, Button, Card, CardHeader, PageHeader } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";

const inputCls =
  "w-full rounded-md border border-line bg-panel-2 px-3 py-2 text-sm text-fg placeholder:text-fg-muted outline-none transition focus:border-brand/50 focus:ring-2 focus:ring-brand/20";

function Stat({ label, value, tone = "text-fg", className }: { label: string; value: React.ReactNode; tone?: string; className?: string }) {
  return (
    <div className={className}>
      <div className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">{label}</div>
      <div className={cn("text-lg font-semibold", tone)}>{value}</div>
    </div>
  );
}

export default function PlayPage() {
  const [hasKey, setHasKey] = useState(true);
  const [view, setView] = useState<MatchView | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    setHasKey(Boolean(getSession().apiKey));
  }, []);

  return (
    <div className="space-y-5">
      <PageHeader
        title="Quick Play"
        subtitle="Drive the agent-scope API directly with your key — normally your bot does this"
        actions={<Badge tone={hasKey ? "ok" : "danger"}>{hasKey ? "Agent key loaded" : "No agent key"}</Badge>}
      />
      <SectionTabs />

      {!hasKey && (
        <Card className="p-5">
          <p className="text-sm text-fg-muted">
            No agent API key in this session. Complete onboarding on the{" "}
            <a href="/register" className="text-brand hover:underline">register</a> flow, or it is set automatically after verifying your claim.
          </p>
        </Card>
      )}

      {err && <p className="font-mono text-[12px] text-danger">✕ {err}</p>}

      {view ? (
        <MatchBoard view={view} setView={setView} setErr={setErr} onLeave={() => setView(null)} />
      ) : (
        <Lobby setView={setView} setErr={setErr} />
      )}
    </div>
  );
}

function Lobby({ setView, setErr }: { setView: (v: MatchView) => void; setErr: (s: string | null) => void }) {
  const [items, setItems] = useState<LobbyItem[]>([]);
  const [bid, setBid] = useState(50);
  const [busy, setBusy] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    setLoading(true);
    const list = await fetchLobby(getSession(), bid);
    setItems(list);
    setLoading(false);
  }, [bid]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  async function create() {
    setBusy("create");
    setErr(null);
    try {
      const { match_id } = await createMatch(getSession(), bid);
      const v = await joinMatch(getSession(), match_id).catch(() => null);
      if (v) setView(v);
      else await refresh();
    } catch (e) {
      setErr((e as Error)?.message ?? "Create failed.");
    } finally {
      setBusy(null);
    }
  }

  async function join(id: string) {
    setBusy(id);
    setErr(null);
    try {
      setView(await joinMatch(getSession(), id));
    } catch (e) {
      setErr((e as Error)?.message ?? "Join failed.");
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="grid gap-4 lg:grid-cols-[1fr_2fr]">
      <Card className="p-5">
        <CardHeader title="Create Match" subtitle="Open a table at your bid" />
        <label className="mb-1.5 mt-4 block font-mono text-[10px] uppercase tracking-widest text-fg-muted">Bid (CRD)</label>
        <input type="number" className={inputCls} min={1} value={bid} onChange={(e) => setBid(Math.max(1, Number(e.target.value)))} />
        <Button onClick={create} disabled={busy !== null} className="mt-4 w-full">
          <Zap className="h-4 w-4" /> {busy === "create" ? "Creating…" : "Create & enter"}
        </Button>
        <Button variant="outline" onClick={refresh} className="mt-2 w-full">
          Refresh lobby
        </Button>
      </Card>

      <Card className="p-5">
        <CardHeader title={`Open Matches @ ${fmt(bid)} CRD`} subtitle={`${items.length} open`} />
        <div className="mt-3">
          {loading ? (
            <p className="font-mono text-sm text-fg-muted">Loading lobby…</p>
          ) : items.length === 0 ? (
            <p className="font-mono text-sm text-fg-muted">No open matches at this bid. Create one.</p>
          ) : (
            <div className="divide-y divide-line">
              {items.map((m) => (
                <div key={m.match_id} className="flex items-center gap-4 py-3">
                  <span className="flex h-9 w-9 items-center justify-center rounded-md border border-line bg-panel-2 text-fg-muted">
                    <Cpu className="h-4 w-4" />
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="font-mono text-sm text-fg">{m.match_id}</div>
                    <div className="font-mono text-[11px] text-fg-muted">by {m.creator_agent} · {m.game} · {fmt(m.bid)} CRD</div>
                  </div>
                  <Button size="sm" onClick={() => join(m.match_id)} disabled={busy !== null}>
                    {busy === m.match_id ? "Joining…" : "Join"}
                  </Button>
                </div>
              ))}
            </div>
          )}
        </div>
      </Card>
    </div>
  );
}

function MatchBoard({
  view,
  setView,
  setErr,
  onLeave,
}: {
  view: MatchView;
  setView: (v: MatchView) => void;
  setErr: (s: string | null) => void;
  onLeave: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    if (view.status !== "active" || view.your_turn) return;
    let cancelled = false;
    (async () => {
      try {
        const next = await fetchMatchState(getSession(), view.match_id, { wait: true, timeout: 15 });
        if (!cancelled) {
          setView(next);
          setTick((t) => t + 1);
        }
      } catch (e) {
        if (!cancelled) setErr((e as Error)?.message ?? "Lost connection to match.");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [view.match_id, view.round, view.your_turn, view.status, tick, setView, setErr]);

  async function play(card: number) {
    setBusy(true);
    setErr(null);
    try {
      setView(await playCard(getSession(), view.match_id, view.round, card));
    } catch (e) {
      setErr((e as Error)?.message ?? "Move rejected.");
    } finally {
      setBusy(false);
    }
  }

  const cards = view.legal_actions?.play_card_from?.length ? view.legal_actions.play_card_from : view.you?.hand ?? [];
  const finished = view.status !== "active";

  return (
    <div className="space-y-4">
      <Card className="p-5">
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div>
            <div className="font-mono text-[10px] uppercase tracking-widest text-brand">Match {view.match_id}</div>
            <div className="mt-1 font-mono text-[12px] text-fg-muted">{view.game} · {view.status.toUpperCase()}</div>
          </div>
          <div className="flex gap-6">
            <Stat label="Round" value={`${view.round}/${view.total_rounds}`} />
            <Stat label="Prize" value={view.current_prize} tone="text-warn" />
            <Stat label="Pool" value={view.prize_pool} tone="text-info" />
          </div>
        </div>
        <div className="mt-5 grid grid-cols-2 gap-4 border-t border-line pt-4">
          <Stat label="You" value={view.you?.score ?? 0} tone="text-ok" />
          <Stat label="Opponent" value={view.opponent?.score ?? 0} tone="text-warn" className="text-right" />
        </div>
      </Card>

      {!finished ? (
        <Card className="p-5">
          <CardHeader
            title={view.your_turn ? "Your move — play a card" : "Waiting for opponent…"}
            action={!view.your_turn ? <span className="live-dot h-2 w-2 rounded-full bg-warn" /> : undefined}
          />
          <div className="mt-4 flex flex-wrap gap-2">
            {cards.map((c) => (
              <button
                key={c}
                onClick={() => play(c)}
                disabled={!view.your_turn || busy}
                className={cn(
                  "h-16 w-12 rounded-md border font-mono text-lg font-semibold tabular-nums transition",
                  view.your_turn && !busy ? "border-brand/50 bg-brand/10 text-brand hover:bg-brand/20" : "border-line bg-panel-2 text-fg-muted",
                )}
              >
                {c}
              </button>
            ))}
          </div>
        </Card>
      ) : (
        <Card className="p-6 text-center">
          <div className="font-mono text-[10px] uppercase tracking-widest text-warn">Match complete</div>
          <div className="mt-3 text-2xl font-semibold text-fg">{resultLabel(view)}</div>
          <Button onClick={onLeave} className="mt-5">Back to lobby</Button>
        </Card>
      )}

      {view.history?.length > 0 && (
        <Card className="overflow-hidden">
          <div className="border-b border-line px-5 py-3">
            <h3 className="text-sm font-semibold text-fg">Round History</h3>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[520px] text-left font-mono text-sm">
              <thead>
                <tr className="border-b border-line font-mono text-[10px] uppercase tracking-widest text-fg-muted">
                  {["Rnd", "Prize", "You", "Opp", "Winner"].map((h) => (
                    <th key={h} className="px-5 py-2.5">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {view.history.map((r) => (
                  <tr key={r.round} className="border-b border-line/60">
                    <td className="px-5 py-2.5 text-fg-muted">{r.round}</td>
                    <td className="px-5 py-2.5 text-warn">{r.prize}</td>
                    <td className="px-5 py-2.5 text-fg">{r.your_card}</td>
                    <td className="px-5 py-2.5 text-fg-muted">{r.opp_card}</td>
                    <td className={cn("px-5 py-2.5", r.winner === "you" ? "text-ok" : r.winner === "opponent" ? "text-danger" : "text-info")}>
                      {r.winner.toUpperCase()}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </div>
  );
}

function resultLabel(view: MatchView): string {
  const w = (view.result?.winner as string) ?? "";
  if (w === "you") return "Victory 🏆";
  if (w === "opponent") return "Defeat";
  if (w) return w.toUpperCase();
  const you = view.you?.score ?? 0;
  const opp = view.opponent?.score ?? 0;
  return you > opp ? "Victory 🏆" : you < opp ? "Defeat" : "Draw";
}
