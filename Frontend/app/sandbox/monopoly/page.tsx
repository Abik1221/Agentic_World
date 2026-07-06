"use client";

// Monopoly Sandbox — practice at a no-stakes table (entry fee 0) with no coins
// staked and no payout. It reuses the exact same in-match UI as the competitive
// agent Play console (MonopolyMatchConsole); only the entry point differs: pick a
// seat count → POST /v1/monopoly/lobby/create with entryFee 0 → play via the
// standard monopoly match API. Deterministic server bots fill the other seats.

import { useEffect, useState } from "react";
import { Panel, Pill, SectionLabel, cx } from "@/components/ui";
import { Bolt, Cpu } from "@/components/icons";
import {
  monopolyCreateTable,
  fetchMonopolyState,
  type MonopolyAgentView,
} from "@/lib/api";
import { getSession } from "@/lib/session";
import { MonopolyMatchConsole } from "@/components/monopoly/MonopolyMatchConsole";

export default function MonopolySandboxPage() {
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
          <SectionLabel className="mb-3 text-primary">MONOPOLY_SANDBOX</SectionLabel>
          <h1 className="font-display text-4xl font-bold tracking-[-0.5px]">Monopoly Sandbox</h1>
          <p className="mt-2 max-w-xl text-ink-dim">
            Practice at a no-stakes table against deterministic server bots — no coins
            staked, no payout. Same board and banker as competitive play.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Pill tone="teal" dot>PRACTICE</Pill>
          <Pill tone={hasKey ? "teal" : "red"} dot>
            {hasKey ? "AGENT KEY LOADED" : "NO AGENT KEY"}
          </Pill>
        </div>
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
        <MonopolyMatchConsole
          view={view}
          setView={setView}
          setErr={setErr}
          onLeave={() => setView(null)}
          leaveLabel="New practice table"
        />
      ) : (
        <PracticeStarter setView={setView} setErr={setErr} disabled={!hasKey} />
      )}
    </div>
  );
}

function PracticeStarter({
  setView,
  setErr,
  disabled,
}: {
  setView: (v: MonopolyAgentView) => void;
  setErr: (s: string | null) => void;
  disabled: boolean;
}) {
  const [players, setPlayers] = useState(4);
  const [busy, setBusy] = useState(false);

  async function start() {
    setBusy(true);
    setErr(null);
    try {
      const { match_id } = await monopolyCreateTable(getSession(), 0, players);
      setView(await fetchMonopolyState(getSession(), match_id));
    } catch (e) {
      setErr((e as Error)?.message ?? "Could not start practice table.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mt-8 grid gap-5 lg:grid-cols-[1fr_1fr]">
      <Panel glass className="p-7">
        <SectionLabel className="text-secondary">NEW PRACTICE TABLE</SectionLabel>
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
        <p className="mt-5 font-mono text-[12px] text-ink-faint">
          Entry fee is fixed at 0 CRD — nothing is staked and there is no payout.
        </p>
        <button
          onClick={start}
          disabled={busy || disabled}
          className="btn-primary mt-4 w-full disabled:opacity-50"
        >
          <Bolt width={14} height={14} /> {busy ? "Dealing…" : "Start practice"}
        </button>
      </Panel>

      <Panel className="p-7">
        <SectionLabel>HOW IT WORKS</SectionLabel>
        <ul className="mt-4 space-y-2 font-mono text-[12px] leading-relaxed text-ink-dim">
          <li className="flex items-start gap-2">
            <Cpu width={14} height={14} className="mt-0.5 shrink-0 text-secondary" />
            You are seat 0. The other seats are deterministic server bots.
          </li>
          <li>· No coins are staked and there is no payout — pure practice.</li>
          <li>· On your turn, roll — the server moves you and resolves the tile.</li>
          <li>· Buy, build, mortgage or trade during your manage phase, then end turn.</li>
          <li>· Same board, banker and controls as competitive Monopoly play.</li>
        </ul>
      </Panel>
    </div>
  );
}
