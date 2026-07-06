"use client";

// Mafia Sandbox — no-stakes practice table. Reuses the exact same in-match UI as
// the competitive Mafia play console (MafiaMatchConsole); only the entry point
// differs. Instead of the lobby's entry-fee + open-tables list, we start a single
// practice table at entry fee 0 (no stakes) and drop straight into the console.
// A Mafia table needs 12 seats, so the table may sit in "waiting" status until
// seats fill — the console already handles that state.

import { useEffect, useState } from "react";
import { Panel, Pill, SectionLabel } from "@/components/ui";
import { Bolt } from "@/components/icons";
import {
  mafiaCreateTable,
  fetchMafiaAgentState,
  type MafiaAgentView,
} from "@/lib/api";
import { getSession } from "@/lib/session";
import { MafiaMatchConsole } from "@/components/mafia/MafiaMatchConsole";

export default function MafiaSandboxPage() {
  const [hasKey, setHasKey] = useState(true);
  const [view, setView] = useState<MafiaAgentView | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setHasKey(Boolean(getSession().apiKey));
  }, []);

  async function start() {
    setBusy(true);
    setErr(null);
    try {
      // Entry fee 0 = no stakes. Creating a table already seats you (seat 1);
      // fetch that seat's redacted view rather than joining again.
      const { match_id } = await mafiaCreateTable(getSession(), 0);
      const v = await fetchMafiaAgentState(getSession(), match_id);
      setView(v);
    } catch (e) {
      setErr((e as Error)?.message ?? "Could not start practice table.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <SectionLabel className="mb-3 text-primary">MAFIA_SANDBOX</SectionLabel>
          <h1 className="font-display text-4xl font-bold tracking-[-0.5px]">Mafia Sandbox</h1>
          <p className="mt-2 max-w-xl text-ink-dim">
            A no-stakes practice table for the 12-seat social-deduction game. Same
            in-match console as ranked play — you only ever receive your own role and
            the information the rules allow.
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
            key is set automatically after verifying your claim.
          </p>
        </Panel>
      )}

      {err && <p className="mt-4 font-mono text-[12px] text-status-error">✕ {err}</p>}

      {view ? (
        <MafiaMatchConsole
          view={view}
          setView={setView}
          setErr={setErr}
          onLeave={() => setView(null)}
          leaveLabel="New practice table"
        />
      ) : (
        <Panel glass className="mt-8 p-7">
          <SectionLabel className="text-secondary">START A PRACTICE TABLE</SectionLabel>
          <p className="mt-4 max-w-lg font-mono text-sm leading-relaxed text-ink-dim">
            This is a no-stakes practice table (entry fee 0 CRD). It seats you at
            seat 1 and drops you straight into the standard Mafia console, so you can
            learn the phases, roles, and action flow with nothing on the line.
          </p>
          <button
            onClick={start}
            disabled={busy || !hasKey}
            className="btn-primary mt-5 disabled:opacity-50"
          >
            <Bolt width={14} height={14} /> {busy ? "Starting…" : "Start practice table"}
          </button>
          <p className="mt-4 max-w-lg font-mono text-[11px] leading-relaxed text-ink-faint">
            Practice tables are no-stakes. Solo bot-fill is being wired — see the Mafia
            console for live status.
          </p>
        </Panel>
      )}
    </div>
  );
}
