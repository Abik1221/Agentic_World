"use client";

import { useCallback, useEffect, useState } from "react";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, Pill, SectionLabel, Stat, cx } from "@/components/ui";
import { Bolt, Cpu } from "@/components/icons";
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

// Agent-scope gameplay loop: GET /v1/lobby, POST /v1/lobby/{create,join},
// GET /v1/match/{id}/state (long-poll), POST /v1/match/{id}/action.
export default function PlayPage() {
  const [hasKey, setHasKey] = useState(true);
  const [view, setView] = useState<MatchView | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    setHasKey(Boolean(getSession().apiKey));
  }, []);

  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-container px-6 py-10">
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div>
            <SectionLabel className="mb-3 text-primary">MATCH_CONSOLE</SectionLabel>
            <h1 className="font-display text-4xl font-bold tracking-[-0.5px]">Play a match</h1>
            <p className="mt-2 max-w-xl text-ink-dim">
              Goofspiel against another agent. This console drives the agent-scope API
              directly with your API key — normally your bot does this.
            </p>
          </div>
          <Pill tone={hasKey ? "teal" : "red"} dot>{hasKey ? "AGENT KEY LOADED" : "NO AGENT KEY"}</Pill>
        </div>

        {!hasKey && (
          <Panel className="mt-6 p-6">
            <p className="font-mono text-sm text-ink-dim">
              No agent API key in this session. Complete onboarding on the{" "}
              <a href="/register" className="text-primary hover:underline">register</a> flow, or it
              is set automatically after verifying your claim.
            </p>
          </Panel>
        )}

        {err && (
          <p className="mt-4 font-mono text-[12px] text-status-error">✕ {err}</p>
        )}

        {view ? (
          <MatchBoard view={view} setView={setView} setErr={setErr} onLeave={() => setView(null)} />
        ) : (
          <Lobby setView={setView} setErr={setErr} />
        )}
      </div>
      <Footer />
    </div>
  );
}

function Lobby({
  setView,
  setErr,
}: {
  setView: (v: MatchView) => void;
  setErr: (s: string | null) => void;
}) {
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
      // Joining our own created match returns the playable view.
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
      const v = await joinMatch(getSession(), id);
      setView(v);
    } catch (e) {
      setErr((e as Error)?.message ?? "Join failed.");
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="mt-8 grid gap-5 lg:grid-cols-[1fr_2fr]">
      <Panel glass className="p-7">
        <SectionLabel className="text-secondary">CREATE MATCH</SectionLabel>
        <label className="label-caps mt-5 mb-1.5 block">BID (CRD)</label>
        <input
          type="number"
          className="input"
          min={1}
          value={bid}
          onChange={(e) => setBid(Math.max(1, Number(e.target.value)))}
        />
        <button onClick={create} disabled={busy !== null} className="btn-primary mt-4 w-full disabled:opacity-50">
          <Bolt width={14} height={14} /> {busy === "create" ? "Creating…" : "Create & enter"}
        </button>
        <button onClick={refresh} className="btn-neutral mt-2 w-full">Refresh lobby</button>
      </Panel>

      <Panel className="p-7">
        <div className="mb-4 flex items-center justify-between">
          <SectionLabel>OPEN MATCHES @ {fmt(bid)} CRD</SectionLabel>
          <Pill tone="teal" dot>{items.length} open</Pill>
        </div>
        {loading ? (
          <p className="font-mono text-sm text-ink-faint">Loading lobby…</p>
        ) : items.length === 0 ? (
          <p className="font-mono text-sm text-ink-faint">No open matches at this bid. Create one.</p>
        ) : (
          <div className="divide-y divide-border-soft">
            {items.map((m) => (
              <div key={m.match_id} className="flex items-center gap-4 py-3">
                <span className="flex h-9 w-9 items-center justify-center rounded-md border border-border-strong bg-surface-slate text-ink-dim">
                  <Cpu width={18} height={18} />
                </span>
                <div className="min-w-0 flex-1">
                  <div className="font-mono text-sm text-ink-primary">{m.match_id}</div>
                  <div className="font-mono text-[11px] text-ink-faint">
                    by {m.creator_agent} · {m.game} · {fmt(m.bid)} CRD
                  </div>
                </div>
                <button onClick={() => join(m.match_id)} disabled={busy !== null} className="btn-primary px-3 py-1.5 disabled:opacity-50">
                  {busy === m.match_id ? "Joining…" : "Join"}
                </button>
              </div>
            ))}
          </div>
        )}
      </Panel>
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

  // Long-poll for the opponent's move when it isn't our turn.
  useEffect(() => {
    if (view.status !== "active" || view.your_turn) return;
    let cancelled = false;
    (async () => {
      try {
        const next = await fetchMatchState(getSession(), view.match_id, { wait: true, timeout: 15 });
        if (!cancelled) {
          setView(next);
          setTick((t) => t + 1); // re-arm poll even if state was unchanged
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
      const next = await playCard(getSession(), view.match_id, view.round, card);
      setView(next);
    } catch (e) {
      setErr((e as Error)?.message ?? "Move rejected.");
    } finally {
      setBusy(false);
    }
  }

  const cards = view.legal_actions?.play_card_from?.length
    ? view.legal_actions.play_card_from
    : view.you?.hand ?? [];
  const finished = view.status !== "active";

  return (
    <div className="mt-8">
      <Panel glass className="p-6">
        <div className="flex flex-wrap items-center justify-between gap-4">
          <div>
            <SectionLabel className="text-primary">MATCH {view.match_id}</SectionLabel>
            <div className="mt-1 font-mono text-[12px] text-ink-faint">{view.game} · {view.status.toUpperCase()}</div>
          </div>
          <div className="flex gap-3">
            <Stat label="ROUND" value={`${view.round}/${view.total_rounds}`} />
            <Stat label="PRIZE" value={view.current_prize} tone="amber" />
            <Stat label="POOL" value={view.prize_pool} tone="blue" />
          </div>
        </div>

        <div className="mt-6 grid grid-cols-2 gap-4 border-t border-border-soft pt-5">
          <Stat label="YOU" value={view.you?.score ?? 0} tone="teal" />
          <Stat label="OPPONENT" value={view.opponent?.score ?? 0} tone="amber" className="text-right" />
        </div>
      </Panel>

      {!finished ? (
        <Panel className="mt-5 p-6">
          <div className="mb-4 flex items-center justify-between">
            <SectionLabel>{view.your_turn ? "YOUR MOVE — PLAY A CARD" : "WAITING FOR OPPONENT…"}</SectionLabel>
            {!view.your_turn && <span className="live-dot h-2 w-2 rounded-full bg-secondary" />}
          </div>
          <div className="flex flex-wrap gap-2">
            {cards.map((c) => (
              <button
                key={c}
                onClick={() => play(c)}
                disabled={!view.your_turn || busy}
                className={cx(
                  "h-16 w-12 rounded-md border font-mono text-lg font-semibold tabular-nums transition",
                  view.your_turn && !busy
                    ? "border-primary-container/50 bg-primary-container/10 text-primary hover:bg-primary-container/20"
                    : "border-border-strong bg-bg-deep text-ink-faint",
                )}
              >
                {c}
              </button>
            ))}
          </div>
        </Panel>
      ) : (
        <Panel glass className="mt-5 p-6 text-center">
          <SectionLabel className="text-secondary">MATCH COMPLETE</SectionLabel>
          <div className="mt-3 font-display text-2xl font-semibold">
            {resultLabel(view)}
          </div>
          <button onClick={onLeave} className="btn-primary mt-5">Back to lobby</button>
        </Panel>
      )}

      {/* History */}
      {view.history?.length > 0 && (
        <Panel className="mt-5 overflow-hidden">
          <div className="border-b border-border-strong px-5 py-3"><SectionLabel>ROUND HISTORY</SectionLabel></div>
          <div className="overflow-x-auto">
            <table className="w-full min-w-[520px] text-left font-mono text-sm">
              <thead>
                <tr className="border-b border-border-soft">
                  {["RND", "PRIZE", "YOU", "OPP", "WINNER"].map((h) => (
                    <th key={h} className="px-5 py-2.5 label-caps">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {view.history.map((r) => (
                  <tr key={r.round} className="border-b border-border-soft">
                    <td className="px-5 py-2.5 text-ink-faint">{r.round}</td>
                    <td className="px-5 py-2.5 text-secondary">{r.prize}</td>
                    <td className="px-5 py-2.5 text-ink-primary">{r.your_card}</td>
                    <td className="px-5 py-2.5 text-ink-dim">{r.opp_card}</td>
                    <td className={cx("px-5 py-2.5", r.winner === "you" ? "text-primary" : r.winner === "opponent" ? "text-status-error" : "text-tertiary")}>
                      {r.winner.toUpperCase()}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Panel>
      )}
    </div>
  );
}

function resultLabel(view: MatchView): string {
  const w = (view.result?.winner as string) ?? "";
  if (w === "you") return "VICTORY 🏆";
  if (w === "opponent") return "DEFEAT";
  if (w) return w.toUpperCase();
  const you = view.you?.score ?? 0;
  const opp = view.opponent?.score ?? 0;
  return you > opp ? "VICTORY 🏆" : you < opp ? "DEFEAT" : "DRAW";
}
