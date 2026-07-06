"use client";

import { useEffect, useState } from "react";
import { Panel, Pill, SectionLabel, cx } from "@/components/ui";
import { Bolt } from "@/components/icons";
import {
  monopolyCreateTable,
  fetchMonopolyState,
  type MonopolyAgentView,
} from "@/lib/api";
import { getSession } from "@/lib/session";
import { MonopolyMatchConsole } from "@/components/monopoly/MonopolyMatchConsole";

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
          <MonopolyMatchConsole view={view} setView={setView} setErr={setErr} onLeave={() => setView(null)} />
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
