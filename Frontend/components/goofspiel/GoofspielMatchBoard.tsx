"use client";

// Shared Goofspiel in-match board. Rendered identically by the competitive
// Quick Play console (app/play) and the practice/sandbox console
// (app/sandbox/goofspiel) so both surfaces show exactly the same gaming UI. It
// drives the standard agent-scope match API — GET /v1/match/{id}/state (long
// poll) + POST /v1/match/{id}/action — which is the same wire contract in ranked
// and sandbox (only the match's mode differs server-side).

import * as React from "react";
import { useEffect, useState } from "react";
import { fetchMatchState, playCard, type MatchView } from "@/lib/api";
import { getSession } from "@/lib/session";
import { cn } from "@/lib/cn";
import { Button, Card, CardHeader } from "@/components/console/primitives";

export function Stat({
  label,
  value,
  tone = "text-fg",
  className,
}: {
  label: string;
  value: React.ReactNode;
  tone?: string;
  className?: string;
}) {
  return (
    <div className={className}>
      <div className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">{label}</div>
      <div className={cn("text-lg font-semibold", tone)}>{value}</div>
    </div>
  );
}

export function GoofspielMatchBoard({
  view,
  setView,
  setErr,
  onLeave,
  leaveLabel = "Back to lobby",
  spectate = false,
}: {
  view: MatchView;
  setView: (v: MatchView) => void;
  setErr: (s: string | null) => void;
  onLeave: () => void;
  leaveLabel?: string;
  // spectate: the seat is being driven elsewhere (push-play — the owner's hosted
  // agent decides moves server-side). Hide the play controls and just follow the
  // match: poll continuously regardless of whose turn it is.
  spectate?: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const [tick, setTick] = useState(0);

  useEffect(() => {
    if (view.status !== "active") return;
    // Interactive mode only long-polls while waiting for the opponent; spectate
    // mode polls every state (our seat is driven server-side, so "your_turn" flips
    // without any input here).
    if (!spectate && view.your_turn) return;
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
  }, [view.match_id, view.round, view.your_turn, view.status, tick, spectate, setView, setErr]);

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

      {!finished && spectate ? (
        <Card className="p-5">
          <CardHeader
            title="Your hosted agent is playing"
            action={<span className="live-dot h-2 w-2 rounded-full bg-brand" />}
          />
          <p className="mt-3 font-mono text-[12px] leading-relaxed text-fg-muted">
            The platform is calling your registered endpoint for each move (push-play). This
            board follows the match live — no input needed.
          </p>
          <div className="mt-4 flex flex-wrap gap-2 opacity-60">
            {cards.map((c) => (
              <span
                key={c}
                className="flex h-16 w-12 items-center justify-center rounded-md border border-line bg-panel-2 font-mono text-lg font-semibold tabular-nums text-fg-muted"
              >
                {c}
              </span>
            ))}
          </div>
        </Card>
      ) : !finished ? (
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
          <Button onClick={onLeave} className="mt-5">{leaveLabel}</Button>
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

export function resultLabel(view: MatchView): string {
  const w = (view.result?.winner as string) ?? "";
  if (w === "you") return "Victory 🏆";
  if (w === "opponent") return "Defeat";
  if (w) return w.toUpperCase();
  const you = view.you?.score ?? 0;
  const opp = view.opponent?.score ?? 0;
  return you > opp ? "Victory 🏆" : you < opp ? "Defeat" : "Draw";
}
