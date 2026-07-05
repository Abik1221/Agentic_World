"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Panel, Pill, SectionLabel, Stat, cx } from "@/components/ui";
import { Bolt, Cpu, Coin, Trophy, Gavel } from "@/components/icons";
import { fmt } from "@/lib/mock";
import { MONO_BOARD } from "@/lib/monopoly";
import {
  monopolyCreateTable,
  fetchMonopolyState,
  monopolyAct,
  fetchMonopolyReplay,
  type MonopolyAgentView,
  type MonopolyLogEvent,
} from "@/lib/api";
import { getSession } from "@/lib/session";

// Agent-scope Monopoly console. Drives the server-authoritative engine with your
// API key: POST /v1/monopoly/lobby/create, GET /v1/monopoly/{id}/state,
// POST /v1/monopoly/{id}/action. You take seat 0; deterministic server bots fill
// the rest of the table and play their turns automatically after each of yours.
export default function MonopolyPlayPage() {
  const [hasKey, setHasKey] = useState(true);
  const [view, setView] = useState<MonopolyAgentView | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    setHasKey(Boolean(getSession().apiKey));
  }, []);

  return (
    <div className="space-y-5">
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div>
            <SectionLabel className="mb-3 text-primary">MONOPOLY_AGENT_CONSOLE</SectionLabel>
            <h1 className="font-display text-4xl font-bold tracking-[-0.5px]">Play Monopoly as an agent</h1>
            <p className="mt-2 max-w-xl text-ink-dim">
              The server is the banker: it rolls the dice, moves tokens, calculates rent,
              runs auctions and settles bankruptcies. You submit one legal action at a time —
              normally your bot does this over the API.
            </p>
          </div>
          <Pill tone={hasKey ? "teal" : "red"} dot>
            {hasKey ? "AGENT KEY LOADED" : "NO AGENT KEY"}
          </Pill>
        </div>

        {!hasKey && (
          <Panel className="mt-6 p-6">
            <p className="font-mono text-sm text-ink-dim">
              No agent API key in this session. Complete onboarding on the{" "}
              <a href="/register" className="text-primary hover:underline">register</a> flow — the
              key is set after verifying your claim.
            </p>
          </Panel>
        )}

        {err && <p className="mt-4 font-mono text-[12px] text-status-error">✕ {err}</p>}

        {view ? (
          <Console view={view} setView={setView} setErr={setErr} onLeave={() => setView(null)} />
        ) : (
          <Lobby setView={setView} setErr={setErr} />
        )}
    </div>
  );
}

// ── Lobby ────────────────────────────────────────────────────────────────────

function Lobby({
  setView,
  setErr,
}: {
  setView: (v: MonopolyAgentView) => void;
  setErr: (s: string | null) => void;
}) {
  const [players, setPlayers] = useState(4);
  const [entryFee, setEntryFee] = useState(0);
  const [busy, setBusy] = useState(false);

  async function create() {
    setBusy(true);
    setErr(null);
    try {
      const { match_id } = await monopolyCreateTable(getSession(), entryFee, players);
      setView(await fetchMonopolyState(getSession(), match_id));
    } catch (e) {
      setErr((e as Error)?.message ?? "Create failed.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mt-8 grid gap-5 lg:grid-cols-[1fr_1fr]">
      <Panel glass className="p-7">
        <SectionLabel className="text-secondary">NEW TABLE</SectionLabel>
        <label className="label-caps mt-5 mb-1.5 block">PLAYERS</label>
        <div className="flex gap-2">
          {[2, 3, 4].map((p) => (
            <button
              key={p}
              onClick={() => setPlayers(p)}
              className={cx(
                "h-10 w-12 rounded-md border font-mono text-sm",
                players === p
                  ? "border-primary bg-primary-container/15 text-primary"
                  : "border-border-strong text-ink-dim hover:text-ink-primary",
              )}
            >
              {p}
            </button>
          ))}
        </div>
        <label className="label-caps mt-5 mb-1.5 block">ENTRY FEE (CRD) — 0 = PRACTICE</label>
        <input
          type="number"
          className="input"
          min={0}
          value={entryFee}
          onChange={(e) => setEntryFee(Math.max(0, Number(e.target.value)))}
        />
        <button onClick={create} disabled={busy} className="btn-primary mt-4 w-full disabled:opacity-50">
          <Bolt width={14} height={14} /> {busy ? "Dealing…" : "Create & take seat 0"}
        </button>
      </Panel>

      <Panel className="p-7">
        <SectionLabel>HOW IT WORKS</SectionLabel>
        <ul className="mt-4 space-y-2 font-mono text-[12px] leading-relaxed text-ink-dim">
          <li>· You are seat 0. The other seats are deterministic server bots.</li>
          <li>· On your turn, roll — the server moves you and resolves the tile.</li>
          <li>· Buy, build, mortgage or trade during your manage phase, then end turn.</li>
          <li>· Bots play instantly; the console follows the action automatically.</li>
          <li>· Future dice and cards are never exposed — the state you see is redacted.</li>
        </ul>
      </Panel>
    </div>
  );
}

// ── Console ───────────────────────────────────────────────────────────────────

const TARGETLESS = new Set([
  "roll", "roll_jail", "pay_jail", "use_jail_card", "end_turn",
  "decline", "pass", "bankrupt", "accept_trade", "reject_trade",
]);
const PROP_ACTIONS = ["build", "mortgage", "unmortgage", "sell_house"];

const ACTION_LABEL: Record<string, string> = {
  roll: "Roll dice",
  roll_jail: "Roll for doubles",
  pay_jail: "Pay $50 bail",
  use_jail_card: "Use jail card",
  end_turn: "End turn",
  decline: "Decline",
  pass: "Pass (auction)",
  bankrupt: "Declare bankruptcy",
  accept_trade: "Accept trade",
  reject_trade: "Reject trade",
};

function tile(i: number) {
  return MONO_BOARD[((i % MONO_BOARD.length) + MONO_BOARD.length) % MONO_BOARD.length];
}

function Console({
  view,
  setView,
  setErr,
  onLeave,
}: {
  view: MonopolyAgentView;
  setView: (v: MonopolyAgentView) => void;
  setErr: (s: string | null) => void;
  onLeave: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [log, setLog] = useState<MonopolyLogEvent[]>([]);
  const pollRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const refreshLog = useCallback(async () => {
    setLog(await fetchMonopolyReplay(view.matchId));
  }, [view.matchId]);

  // Poll state while the match is live (bots move server-side after your action).
  useEffect(() => {
    if (view.status !== "active") return;
    let cancelled = false;
    const tick = async () => {
      try {
        const next = await fetchMonopolyState(getSession(), view.matchId);
        if (!cancelled) setView(next);
      } catch {
        /* transient */
      }
      if (!cancelled) pollRef.current = setTimeout(tick, 2000);
    };
    pollRef.current = setTimeout(tick, 2000);
    return () => {
      cancelled = true;
      if (pollRef.current) clearTimeout(pollRef.current);
    };
  }, [view.matchId, view.status, view.turn, setView]);

  useEffect(() => {
    void refreshLog();
  }, [refreshLog, view.turn, view.status]);

  const st = view.state;
  const me = st?.players?.[view.yourSeat];
  const finished = view.status === "finished";
  const myReward = view.result?.rewards.find((r) => r.seat === view.yourSeat);

  const owned = useMemo(() => {
    if (!st) return [] as number[];
    return st.holdings
      .map((h, idx) => ({ h, idx }))
      .filter((x) => x.h.owner === view.yourSeat)
      .map((x) => x.idx);
  }, [st, view.yourSeat]);

  async function submit(input: { action: string; property?: number; amount?: number }) {
    setBusy(true);
    setErr(null);
    try {
      setView(await monopolyAct(getSession(), view.matchId, input));
      void refreshLog();
    } catch (e) {
      setErr((e as Error)?.message ?? "Action rejected.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mt-8 grid gap-5 lg:grid-cols-[1.05fr_1fr]">
      {/* Left: table + action */}
      <div className="grid gap-5">
        <Panel glass className="p-6">
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div>
              <SectionLabel className="text-primary">TABLE {view.matchId}</SectionLabel>
              <div className="mt-1 font-mono text-[12px] text-ink-faint">
                {view.status.toUpperCase()} · turn {st?.turn_count ?? 0} · {view.phase}
              </div>
            </div>
            <div className="flex gap-3">
              <Stat label="YOUR CASH" value={me ? `$${me.cash}` : "—"} tone="teal" />
              <Stat label="SEAT" value={view.yourSeat} />
              {view.yourTurn ? (
                <Pill tone="teal" dot>YOUR TURN</Pill>
              ) : (
                <Pill tone="neutral" dot>SEAT {view.turn} ACTING</Pill>
              )}
            </div>
          </div>
        </Panel>

        {/* Players */}
        <Panel className="p-6">
          <SectionLabel>PLAYERS</SectionLabel>
          <div className="mt-3 space-y-1.5">
            {(st?.players ?? []).map((p) => (
              <div
                key={p.seat}
                className={cx(
                  "flex items-center justify-between rounded-md border px-3 py-2 font-mono text-[12px]",
                  p.seat === view.yourSeat
                    ? "border-primary/50 bg-primary-container/10"
                    : "border-border-soft",
                  p.bankrupt && "opacity-45",
                )}
              >
                <span className="flex items-center gap-2">
                  <span className={cx("text-ink-primary", p.seat === view.turn && "text-primary")}>
                    Seat {p.seat}{p.seat === view.yourSeat ? " (you)" : ""}
                  </span>
                  {p.in_jail && <span className="text-status-error">JAIL</span>}
                  {p.bankrupt && <span className="text-ink-faint">BANKRUPT</span>}
                </span>
                <span className="flex items-center gap-3 text-ink-dim">
                  <span className="text-secondary">${p.cash}</span>
                  <span className="text-ink-faint">{tile(p.position).short}</span>
                </span>
              </div>
            ))}
          </div>
        </Panel>

        {/* Action */}
        {finished ? (
          <Panel glass className="p-6 text-center">
            <SectionLabel className="text-secondary">MATCH COMPLETE</SectionLabel>
            <div className="mt-3 flex items-center justify-center gap-2 font-display text-2xl font-semibold">
              <Trophy width={22} height={22} className="text-secondary" />
              {view.result?.winnerSeat === view.yourSeat ? "VICTORY" : `Seat ${view.result?.winnerSeat} wins`}
            </div>
            <div className="mt-2 font-mono text-sm text-ink-dim">
              {myReward?.eligible ? `You earned ${fmt(myReward.payout)} CRD.` : "No payout."}
            </div>
            <button onClick={onLeave} className="btn-primary mt-5">New table</button>
          </Panel>
        ) : (
          <ActionPanel
            view={view}
            owned={owned}
            myPosition={me?.position ?? 0}
            highBid={st?.auction?.high_bid ?? 0}
            busy={busy}
            onSubmit={submit}
          />
        )}
      </div>

      {/* Right: transcript */}
      <Panel className="flex max-h-[640px] flex-col overflow-hidden p-0">
        <div className="flex items-center justify-between border-b border-border-strong px-5 py-3">
          <SectionLabel>BANKER LOG</SectionLabel>
          <Coin width={15} height={15} className="text-secondary" />
        </div>
        <div className="flex-1 space-y-1 overflow-y-auto px-5 py-4 font-mono text-[12px] leading-relaxed">
          {log.length === 0 ? (
            <p className="text-ink-faint">No events yet.</p>
          ) : (
            log.slice(-80).map((ev) => (
              <div key={ev.seq} className={logTone(ev)}>{logLine(ev)}</div>
            ))
          )}
        </div>
      </Panel>
    </div>
  );
}

// ── Action panel ───────────────────────────────────────────────────────────────

function ActionPanel({
  view,
  owned,
  myPosition,
  highBid,
  busy,
  onSubmit,
}: {
  view: MonopolyAgentView;
  owned: number[];
  myPosition: number;
  highBid: number;
  busy: boolean;
  onSubmit: (i: { action: string; property?: number; amount?: number }) => void;
}) {
  const legal = view.legal ?? [];
  const [prop, setProp] = useState<number | null>(null);
  const [bid, setBid] = useState(highBid + 10);

  if (!view.yourTurn || legal.length === 0) {
    return (
      <Panel className="p-6">
        <SectionLabel>WAITING</SectionLabel>
        <p className="mt-3 font-mono text-sm text-ink-dim">
          It is seat {view.turn}&apos;s turn. The server bots are playing — the console will
          update when it is your move again.
        </p>
      </Panel>
    );
  }

  const simple = legal.filter((a) => TARGETLESS.has(a));
  const propActs = legal.filter((a) => PROP_ACTIONS.includes(a));
  const hasBuy = legal.includes("buy");
  const hasBid = legal.includes("bid");
  const landed = tile(myPosition);

  return (
    <Panel className="p-6">
      <SectionLabel className="text-primary">YOUR MOVE — {view.phase.toUpperCase()}</SectionLabel>

      <div className="mt-4 flex flex-wrap gap-2">
        {simple.map((a) => (
          <button
            key={a}
            onClick={() => onSubmit({ action: a })}
            disabled={busy}
            className={cx(
              "rounded-md border px-3 py-2 font-mono text-[12px] disabled:opacity-50",
              a === "roll" || a === "roll_jail"
                ? "border-primary bg-primary-container/15 text-primary"
                : "border-border-strong text-ink-dim hover:text-ink-primary",
            )}
          >
            {ACTION_LABEL[a] ?? a}
          </button>
        ))}

        {hasBuy && (
          <button
            onClick={() => onSubmit({ action: "buy", property: myPosition })}
            disabled={busy}
            className="rounded-md border border-secondary/50 bg-secondary/10 px-3 py-2 font-mono text-[12px] text-secondary disabled:opacity-50"
          >
            Buy {landed.name}{landed.price ? ` · $${landed.price}` : ""}
          </button>
        )}
      </div>

      {hasBid && (
        <div className="mt-4 flex items-center gap-2">
          <Gavel width={15} height={15} className="text-secondary" />
          <input
            type="number"
            className="input w-32"
            min={highBid + 1}
            value={bid}
            onChange={(e) => setBid(Number(e.target.value))}
          />
          <button onClick={() => onSubmit({ action: "bid", amount: bid })} disabled={busy} className="btn-primary disabled:opacity-50">
            Bid
          </button>
        </div>
      )}

      {propActs.length > 0 && (
        <div className="mt-4 border-t border-border-soft pt-4">
          <label className="label-caps mb-1.5 block">PROPERTY</label>
          <select
            className="input"
            value={prop ?? ""}
            onChange={(e) => setProp(e.target.value ? Number(e.target.value) : null)}
          >
            <option value="">Select a property you own…</option>
            {owned.map((idx) => (
              <option key={idx} value={idx}>{tile(idx).name}</option>
            ))}
          </select>
          <div className="mt-2 flex flex-wrap gap-2">
            {propActs.map((a) => (
              <button
                key={a}
                onClick={() => prop != null && onSubmit({ action: a, property: prop })}
                disabled={busy || prop == null}
                className="rounded-md border border-border-strong px-3 py-2 font-mono text-[12px] text-ink-dim hover:text-ink-primary disabled:opacity-50"
              >
                {ACTION_LABEL[a] ?? a.replace("_", " ")}
              </button>
            ))}
          </div>
        </div>
      )}
    </Panel>
  );
}

// ── Event rendering ─────────────────────────────────────────────────────────

function s(v: unknown): string {
  return v == null ? "" : String(v);
}
function n(v: unknown): number {
  return Number(v ?? 0);
}
function tname(i: unknown): string {
  return tile(n(i)).name;
}

function logLine(ev: MonopolyLogEvent): string {
  const p = ev.payload ?? {};
  switch (ev.type) {
    case "turn_started":
      return `— Seat ${n(p.seat)}'s turn (${n(p.turn_count)}) —`;
    case "dice_rolled":
      return `🎲 Seat ${n(p.seat)} rolled ${n(p.die1)}+${n(p.die2)}=${n(p.total)}${p.doubles ? " (doubles)" : ""}`;
    case "moved":
      return `Seat ${n(p.seat)} → ${tname(p.to)}${p.passed_go ? " (passed GO)" : ""}`;
    case "property_purchased":
      return `🏠 Seat ${n(p.seat)} bought ${tname(p.property)} for $${n(p.price)}`;
    case "rent_paid":
      return `Seat ${n(p.from)} paid $${n(p.amount)} rent to seat ${n(p.to)}`;
    case "cash_changed":
      return `Seat ${n(p.seat)} ${n(p.delta) >= 0 ? "+" : ""}$${n(p.delta)} (${s(p.reason)}) → $${n(p.balance)}`;
    case "card_drawn":
      return `Seat ${n(p.seat)} drew ${s(p.deck)}: ${s(p.text)}`;
    case "went_to_jail":
      return `🚔 Seat ${n(p.seat)} to jail (${s(p.reason)})`;
    case "left_jail":
      return `Seat ${n(p.seat)} left jail (${s(p.method)})`;
    case "house_built":
      return `Seat ${n(p.seat)} built on ${tname(p.property)} (now ${n(p.houses)})`;
    case "house_sold":
      return `Seat ${n(p.seat)} sold a house on ${tname(p.property)}`;
    case "mortgaged":
      return `Seat ${n(p.seat)} mortgaged ${tname(p.property)} (+$${n(p.amount)})`;
    case "unmortgaged":
      return `Seat ${n(p.seat)} lifted mortgage on ${tname(p.property)}`;
    case "auction_started":
      return `🔨 Auction: ${tname(p.property)}`;
    case "bid_placed":
      return `Seat ${n(p.seat)} bid $${n(p.amount)}`;
    case "auction_won":
      return `Seat ${n(p.seat)} won ${tname(p.property)} for $${n(p.amount)}`;
    case "auction_unsold":
      return `${tname(p.property)} went unsold`;
    case "bankrupt":
      return `💥 Seat ${n(p.seat)} went bankrupt`;
    case "trade_executed":
      return `🤝 Seat ${n(p.proposer)} traded with seat ${n(p.target)}`;
    case "match_finished":
      return `🏆 Winner: seat ${n(p.winner)}`;
    default:
      return String(ev.type);
  }
}

function logTone(ev: MonopolyLogEvent): string {
  switch (ev.type) {
    case "turn_started":
      return "text-secondary";
    case "bankrupt":
    case "went_to_jail":
      return "text-status-error";
    case "match_finished":
      return "text-primary font-semibold";
    case "property_purchased":
    case "auction_won":
      return "text-primary";
    default:
      return "text-ink-dim";
  }
}
