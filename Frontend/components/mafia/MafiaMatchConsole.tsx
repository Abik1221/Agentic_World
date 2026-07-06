"use client";

// Shared Mafia in-match console. Rendered identically by the competitive Mafia
// play console (app/mafia/play) and the practice/sandbox console
// (app/sandbox/mafia) so both surfaces show exactly the same gaming UI. It drives
// the redacted agent-scope engine API directly with your API key:
// GET /v1/mafia/{id}/state (light poll while live), POST /v1/mafia/{id}/action,
// and GET /v1/mafia/{id}/replay once finished. The server only ever returns what
// THIS seat is allowed to know — its own role, its fellow-Mafia allies, the public
// transcript, and its own private night results.

import { useEffect, useMemo, useRef, useState } from "react";
import { Panel, Pill, SectionLabel, Stat, cx } from "@/components/ui";
import { Skull, Search, Cross, Shield, Users, Gavel, Moon, Sun, Trophy } from "@/components/icons";
import { fmt } from "@/lib/mock";
import {
  fetchMafiaAgentState,
  fetchMafiaReplay,
  mafiaAct,
  type MafiaAgentView,
  type MafiaLogEvent,
} from "@/lib/api";
import { getSession } from "@/lib/session";

// ── Console ───────────────────────────────────────────────────────────────────

const ROLE_ICON: Record<string, typeof Skull> = {
  Mafia: Skull,
  Detective: Search,
  Doctor: Cross,
  Sheriff: Shield,
  Villager: Users,
};

const PHASE_ICON: Record<string, typeof Moon> = {
  night: Moon,
  morning: Sun,
  discussion: Users,
  voting: Gavel,
  result: Trophy,
};

export function MafiaMatchConsole({
  view,
  setView,
  setErr,
  onLeave,
  leaveLabel = "Back to lobby",
  spectate = false,
}: {
  view: MafiaAgentView;
  setView: (v: MafiaAgentView) => void;
  setErr: (s: string | null) => void;
  onLeave: () => void;
  leaveLabel?: string;
  // spectate: this seat is driven server-side (push-play). Replace the action
  // card with a "your agent is playing" note; the poll loop already follows along.
  spectate?: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const [reveal, setReveal] = useState<MafiaLogEvent[] | null>(null);
  const pollRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Once the match is finished the backend un-redacts /replay: fetch the full
  // log to reveal the night actions (and therefore every role) that were hidden
  // during play — the spec's "Role Assignment, hidden until match end".
  useEffect(() => {
    if (view.status !== "finished") return;
    let cancelled = false;
    fetchMafiaReplay(view.matchId).then((evs) => {
      if (!cancelled) setReveal(evs);
    });
    return () => {
      cancelled = true;
    };
  }, [view.status, view.matchId]);

  // Poll the redacted state while the match is live. State is not long-poll on
  // this endpoint, so a light interval keeps the console in sync as bots act.
  useEffect(() => {
    // Poll while the table is filling (waiting) or in play (active) so the
    // console follows the transition to the first night and every bot action.
    if (view.status !== "active" && view.status !== "waiting") return;
    let cancelled = false;
    const tick = async () => {
      try {
        const next = await fetchMafiaAgentState(getSession(), view.matchId);
        if (!cancelled) setView(next);
      } catch {
        /* transient; keep the last view */
      }
      if (!cancelled) pollRef.current = setTimeout(tick, 2500);
    };
    pollRef.current = setTimeout(tick, 2500);
    return () => {
      cancelled = true;
      if (pollRef.current) clearTimeout(pollRef.current);
    };
  }, [view.matchId, view.status, view.day, view.phase, setView]);

  const role = view.yourRole ?? "";
  const team = role === "Mafia" ? "mafia" : role ? "town" : "";
  const RoleIcon = ROLE_ICON[role] ?? Users;
  const PhaseIcon = PHASE_ICON[view.phase] ?? Moon;

  const aliveSeats = useMemo(
    () =>
      Object.entries(view.alive)
        .filter(([, ok]) => ok)
        .map(([s]) => Number(s))
        .sort((a, b) => a - b),
    [view.alive],
  );

  const legal = view.legal ?? [];
  const action = legal[0]; // exactly one legal kind per phase for a given seat
  const finished = view.status === "finished";
  const myReward = view.result?.rewards.find((r) => r.seat === view.yourSeat);

  async function submit(input: { action: string; target?: number; tone?: string; text?: string }) {
    setBusy(true);
    setErr(null);
    try {
      setView(await mafiaAct(getSession(), view.matchId, input));
    } catch (e) {
      setErr((e as Error)?.message ?? "Action rejected.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mt-8 grid gap-5 lg:grid-cols-[1.1fr_1fr]">
      {/* Left column: identity + action */}
      <div className="grid gap-5">
        <Panel glass className="p-6">
          <div className="flex flex-wrap items-center justify-between gap-4">
            <div>
              <SectionLabel className="text-primary">TABLE {view.matchId}</SectionLabel>
              <div className="mt-1 font-mono text-[12px] text-ink-faint">
                {view.status.toUpperCase()} · {aliveSeats.length} alive · entry {fmt(view.entryFee)} CRD
              </div>
            </div>
            <div className="flex gap-3">
              <Stat label="DAY" value={view.day} />
              <div className="flex items-center gap-2">
                <PhaseIcon width={16} height={16} className="text-secondary" />
                <Stat label="PHASE" value={view.phase.toUpperCase()} tone="blue" />
              </div>
            </div>
          </div>

          {view.yourSeat > 0 && (
            <div className="mt-5 flex items-center gap-4 border-t border-border-soft pt-5">
              <span
                className={cx(
                  "flex h-11 w-11 items-center justify-center rounded-lg border",
                  team === "mafia"
                    ? "border-status-error/40 bg-status-error/10 text-status-error"
                    : "border-primary-container/40 bg-primary-container/10 text-primary",
                )}
              >
                <RoleIcon width={20} height={20} />
              </span>
              <div>
                <div className="font-mono text-[11px] text-ink-faint">
                  YOU · SEAT {view.yourSeat}
                </div>
                <div className="font-display text-lg font-semibold">
                  {role || "—"}{" "}
                  {team && (
                    <span className={team === "mafia" ? "text-status-error" : "text-primary"}>
                      · {team}
                    </span>
                  )}
                </div>
                {view.allies && view.allies.length > 0 && (
                  <div className="mt-0.5 font-mono text-[11px] text-status-error">
                    Fellow Mafia: seats {view.allies.join(", ")}
                  </div>
                )}
              </div>
            </div>
          )}
        </Panel>

        {/* Action */}
        {view.status === "waiting" ? (
          <Panel className="p-6">
            <SectionLabel className="text-secondary">TABLE FILLING</SectionLabel>
            <p className="mt-3 font-mono text-sm text-ink-dim">
              Waiting for 12 seats. The match starts and roles are dealt the moment the
              table is full — this console will update automatically.
            </p>
          </Panel>
        ) : !finished && spectate ? (
          <Panel className="p-6">
            <SectionLabel className="text-primary">YOUR HOSTED AGENT IS PLAYING</SectionLabel>
            <p className="mt-3 font-mono text-sm leading-relaxed text-ink-dim">
              The platform is calling your registered endpoint for this seat&apos;s moves
              (push-play); rule-based bots fill the other seats. This console follows the
              table live — no input needed.
            </p>
          </Panel>
        ) : !finished ? (
          <ActionCard
            action={action}
            phase={view.phase}
            aliveSeats={aliveSeats}
            yourSeat={view.yourSeat}
            role={role}
            busy={busy}
            onSubmit={submit}
          />
        ) : (
          <Panel glass className="p-6 text-center">
            <SectionLabel className="text-secondary">MATCH COMPLETE</SectionLabel>
            <div className="mt-3 font-display text-2xl font-semibold">
              {view.result?.winner
                ? `${view.result.winner === team ? "VICTORY 🏆" : "DEFEAT"} · ${view.result.winner} wins`
                : "FINISHED"}
            </div>
            <div className="mt-2 font-mono text-sm text-ink-dim">
              {myReward?.eligible
                ? `You earned ${fmt(myReward.payout)} CRD (${myReward.reason}).`
                : myReward
                  ? `No payout — ${myReward.reason}.`
                  : "No payout."}
            </div>
            <button onClick={onLeave} className="btn-primary mt-5">{leaveLabel}</button>
          </Panel>
        )}

        {/* Alive seats */}
        <Panel className="p-6">
          <SectionLabel>SEATS ALIVE</SectionLabel>
          <div className="mt-4 flex flex-wrap gap-2">
            {aliveSeats.map((s) => (
              <span
                key={s}
                className={cx(
                  "flex h-9 w-9 items-center justify-center rounded-md border font-mono text-sm",
                  s === view.yourSeat
                    ? "border-primary bg-primary-container/15 text-primary"
                    : view.allies?.includes(s)
                      ? "border-status-error/40 bg-status-error/10 text-status-error"
                      : "border-border-strong bg-bg-deep text-ink-dim",
                )}
                title={s === view.yourSeat ? "You" : view.allies?.includes(s) ? "Ally (Mafia)" : undefined}
              >
                {s}
              </span>
            ))}
          </div>
        </Panel>
      </div>

      {/* Right column: reveal (post-match) + private results + public transcript */}
      <div className="grid gap-5">
        {finished && reveal && reveal.some((e) => e.type === "night") && (
          <Panel className="p-6">
            <SectionLabel className="text-primary">REVEALED · NIGHT ACTIONS</SectionLabel>
            <p className="mt-2 font-mono text-[11px] text-ink-faint">
              Hidden during play; unsealed now the match is over.
            </p>
            <div className="mt-3 space-y-1.5 font-mono text-[12px]">
              {reveal
                .filter((e) => e.type === "night")
                .map((ev) => (
                  <div key={ev.seq} className="text-ink-dim">
                    {revealLine(ev)}
                  </div>
                ))}
            </div>
          </Panel>
        )}

        {view.private && view.private.length > 0 && (
          <Panel className="p-6">
            <SectionLabel className="text-secondary">YOUR PRIVATE INTEL</SectionLabel>
            <div className="mt-3 space-y-2">
              {view.private.map((ev) => (
                <div key={ev.seq} className="rounded-md border border-status-error/25 bg-status-error/5 px-3 py-2 font-mono text-[12px] text-ink-primary">
                  {privateLine(ev)}
                </div>
              ))}
            </div>
          </Panel>
        )}

        <Panel className="flex max-h-[560px] flex-col overflow-hidden p-0">
          <div className="border-b border-border-strong px-5 py-3">
            <SectionLabel>PUBLIC TRANSCRIPT</SectionLabel>
          </div>
          <div className="flex-1 space-y-1.5 overflow-y-auto px-5 py-4 font-mono text-[12px] leading-relaxed">
            {(view.transcript ?? []).length === 0 ? (
              <p className="text-ink-faint">No public events yet.</p>
            ) : (
              (view.transcript ?? []).map((ev) => (
                <div key={ev.seq} className={transcriptTone(ev)}>
                  {transcriptLine(ev)}
                </div>
              ))
            )}
          </div>
        </Panel>
      </div>
    </div>
  );
}

// ── Action card ────────────────────────────────────────────────────────────────

const TONES = ["info", "accuse", "defend", "claim", "alliance"];

const ACTION_LABEL: Record<string, string> = {
  night_kill: "Choose a target to eliminate",
  investigate: "Choose a seat to investigate",
  protect: "Choose a seat to protect",
  profile: "Choose a seat to profile",
  vote: "Vote to eliminate a seat",
};

function ActionCard({
  action,
  phase,
  aliveSeats,
  yourSeat,
  role,
  busy,
  onSubmit,
}: {
  action: string | undefined;
  phase: string;
  aliveSeats: number[];
  yourSeat: number;
  role: string;
  busy: boolean;
  onSubmit: (i: { action: string; target?: number; tone?: string; text?: string }) => void;
}) {
  const [target, setTarget] = useState<number | null>(null);
  const [tone, setTone] = useState("info");
  const [text, setText] = useState("");

  // Doctor may protect itself; every other targeted action excludes self.
  const targets = useMemo(
    () => (action === "protect" ? aliveSeats : aliveSeats.filter((s) => s !== yourSeat)),
    [action, aliveSeats, yourSeat],
  );

  if (!action) {
    return (
      <Panel className="p-6">
        <SectionLabel>NO ACTION THIS PHASE</SectionLabel>
        <p className="mt-3 font-mono text-sm text-ink-dim">
          {phase === "night" && role && role !== "Mafia" && role !== "Detective" && role !== "Doctor" && role !== "Sheriff"
            ? "Villagers sleep at night. Waiting for the special roles and dawn."
            : "Waiting for the other agents to act, or for the phase to advance."}
        </p>
      </Panel>
    );
  }

  if (action === "message") {
    return (
      <Panel className="p-6">
        <SectionLabel className="text-primary">DISCUSSION — SPEAK</SectionLabel>
        <div className="mt-4 flex flex-wrap gap-2">
          {TONES.map((t) => (
            <button
              key={t}
              onClick={() => setTone(t)}
              className={cx(
                "rounded-md border px-3 py-1 font-mono text-[11px] uppercase",
                tone === t
                  ? "border-primary bg-primary-container/15 text-primary"
                  : "border-border-strong text-ink-dim hover:text-ink-primary",
              )}
            >
              {t}
            </button>
          ))}
        </div>
        <textarea
          className="input mt-3 h-24 resize-none"
          placeholder="Share a read on the table…"
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
        <div className="mt-3 flex items-center gap-3">
          <select
            className="input flex-1"
            value={target ?? ""}
            onChange={(e) => setTarget(e.target.value ? Number(e.target.value) : null)}
          >
            <option value="">No specific target</option>
            {targets.map((s) => (
              <option key={s} value={s}>Address seat {s}</option>
            ))}
          </select>
          <button
            onClick={() => onSubmit({ action, tone, text, target: target ?? undefined })}
            disabled={busy || text.trim() === ""}
            className="btn-primary disabled:opacity-50"
          >
            {busy ? "Sending…" : "Send"}
          </button>
        </div>
      </Panel>
    );
  }

  // Targeted action (night_kill | investigate | protect | profile | vote).
  return (
    <Panel className="p-6">
      <SectionLabel className="text-primary">{ACTION_LABEL[action] ?? action.toUpperCase()}</SectionLabel>
      <div className="mt-4 flex flex-wrap gap-2">
        {targets.map((s) => (
          <button
            key={s}
            onClick={() => setTarget(s)}
            className={cx(
              "h-11 w-11 rounded-md border font-mono text-sm font-semibold tabular-nums transition",
              target === s
                ? "border-primary bg-primary-container/20 text-primary"
                : "border-border-strong bg-bg-deep text-ink-dim hover:text-ink-primary",
            )}
          >
            {s}
          </button>
        ))}
      </div>
      <button
        onClick={() => target != null && onSubmit({ action, target })}
        disabled={busy || target == null}
        className="btn-primary mt-4 disabled:opacity-50"
      >
        {busy ? "Submitting…" : `Confirm${target != null ? ` · seat ${target}` : ""}`}
      </button>
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

function transcriptLine(ev: MafiaLogEvent): string {
  const p = ev.payload ?? {};
  switch (ev.type) {
    case "phase":
      return `— Day ${n(p.day)} · ${s(p.phase).toUpperCase()} —`;
    case "moderator":
      return `▸ ${s(p.text)}`;
    case "message":
      return `Seat ${n(p.from)}${p.target != null ? ` → ${n(p.target)}` : ""} [${s(p.tone)}]: ${s(p.text)}`;
    case "vote":
      return `🗳 Seat ${n(p.from)} votes seat ${n(p.target)}`;
    case "eliminate":
      return `☠ Seat ${n(p.target)} eliminated (${s(p.cause)})`;
    case "victory":
      return `🏆 ${s(p.team).toUpperCase()} wins — ${s(p.text)}`;
    default:
      return s(ev.type);
  }
}

function transcriptTone(ev: MafiaLogEvent): string {
  switch (ev.type) {
    case "phase":
      return "text-secondary";
    case "eliminate":
      return "text-status-error";
    case "victory":
      return "text-primary font-semibold";
    case "moderator":
      return "text-ink-faint";
    default:
      return "text-ink-dim";
  }
}

function privateLine(ev: MafiaLogEvent): string {
  const p = ev.payload ?? {};
  if (p.finding != null && p.target != null) {
    return `You investigated seat ${n(p.target)} → ${s(p.finding)}`;
  }
  if (p.secret != null) return s(p.secret);
  return s(p.text);
}

function revealLine(ev: MafiaLogEvent): string {
  const p = ev.payload ?? {};
  const who = p.seat != null ? `Seat ${n(p.seat)} (${s(p.actor)})` : s(p.actor);
  if (p.finding != null && p.target != null) return `${who} investigated seat ${n(p.target)} → ${s(p.finding)}`;
  if (p.target != null) return `${who} → seat ${n(p.target)}`;
  if (p.secret != null) return `${who}: ${s(p.secret)}`;
  return `${who}: ${s(p.text)}`;
}
