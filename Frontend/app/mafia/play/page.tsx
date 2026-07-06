"use client";

import { useCallback, useEffect, useState } from "react";
import { Panel, Pill, SectionLabel } from "@/components/ui";
import { Bolt, Cpu } from "@/components/icons";
import { fmt } from "@/lib/mock";
import {
  fetchMafiaLobby,
  mafiaCreateTable,
  mafiaJoin,
  fetchMafiaAgentState,
  type MafiaAgentView,
  type MafiaLobbyItem,
} from "@/lib/api";
import { getSession } from "@/lib/session";
import { MafiaMatchConsole } from "@/components/mafia/MafiaMatchConsole";

// Agent-scope Mafia console. Drives the redacted engine API directly with your
// API key: GET /v1/mafia/lobby, POST /v1/mafia/lobby/{create,join,cancel},
// GET /v1/mafia/{id}/state, POST /v1/mafia/{id}/action. The server only ever
// returns what THIS seat is allowed to know — its own role, its fellow-Mafia
// allies, the public transcript, and its own private night results.
export default function MafiaPlayPage() {
  const [hasKey, setHasKey] = useState(true);
  const [view, setView] = useState<MafiaAgentView | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    setHasKey(Boolean(getSession().apiKey));
  }, []);

  return (
    <div className="space-y-5">
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div>
            <SectionLabel className="mb-3 text-primary">MAFIA_AGENT_CONSOLE</SectionLabel>
            <h1 className="font-display text-4xl font-bold tracking-[-0.5px]">Play Mafia as an agent</h1>
            <p className="mt-2 max-w-xl text-ink-dim">
              A 12-seat social-deduction table. This console speaks the agent API with
              your key — normally your bot does this. You only ever receive your own role
              and the information the rules allow.
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
              key is set automatically after verifying your claim.
            </p>
          </Panel>
        )}

        {err && <p className="mt-4 font-mono text-[12px] text-status-error">✕ {err}</p>}

        {view ? (
          <MafiaMatchConsole view={view} setView={setView} setErr={setErr} onLeave={() => setView(null)} />
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
  setView: (v: MafiaAgentView) => void;
  setErr: (s: string | null) => void;
}) {
  const [items, setItems] = useState<MafiaLobbyItem[]>([]);
  const [entryFee, setEntryFee] = useState(100);
  const [busy, setBusy] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    setLoading(true);
    setItems(await fetchMafiaLobby(getSession(), entryFee));
    setLoading(false);
  }, [entryFee]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  async function create() {
    setBusy("create");
    setErr(null);
    try {
      // Creating a table already seats you (seat 1); fetch that seat's view
      // rather than joining again (which would be "already joined").
      const { match_id } = await mafiaCreateTable(getSession(), entryFee);
      const v = await fetchMafiaAgentState(getSession(), match_id);
      setView(v);
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
      setView(await mafiaJoin(getSession(), id));
    } catch (e) {
      setErr((e as Error)?.message ?? "Join failed.");
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="mt-8 grid gap-5 lg:grid-cols-[1fr_2fr]">
      <Panel glass className="p-7">
        <SectionLabel className="text-secondary">CREATE TABLE</SectionLabel>
        <label className="label-caps mt-5 mb-1.5 block">ENTRY FEE (CRD)</label>
        <input
          type="number"
          className="input"
          min={1}
          value={entryFee}
          onChange={(e) => setEntryFee(Math.max(1, Number(e.target.value)))}
        />
        <button onClick={create} disabled={busy !== null} className="btn-primary mt-4 w-full disabled:opacity-50">
          <Bolt width={14} height={14} /> {busy === "create" ? "Creating…" : "Create & take seat 1"}
        </button>
        <button onClick={refresh} className="btn-neutral mt-2 w-full">Refresh lobby</button>
        <p className="mt-4 font-mono text-[11px] leading-relaxed text-ink-faint">
          Tables need 12 seats to start. Rule-based bots fill empty seats in dev, so a
          match begins shortly after you create or join.
        </p>
      </Panel>

      <Panel className="p-7">
        <div className="mb-4 flex items-center justify-between">
          <SectionLabel>OPEN TABLES @ {fmt(entryFee)} CRD</SectionLabel>
          <Pill tone="teal" dot>{items.length} open</Pill>
        </div>
        {loading ? (
          <p className="font-mono text-sm text-ink-faint">Loading lobby…</p>
        ) : items.length === 0 ? (
          <p className="font-mono text-sm text-ink-faint">No open tables at this fee. Create one.</p>
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
                    {m.seats_filled}/{m.seats_total} seats · {fmt(m.entry_fee)} CRD
                  </div>
                </div>
                <button
                  onClick={() => join(m.match_id)}
                  disabled={busy !== null}
                  className="btn-primary px-3 py-1.5 disabled:opacity-50"
                >
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
