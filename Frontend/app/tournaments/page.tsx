"use client";

import { useState } from "react";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, Pill, SectionLabel, Stat } from "@/components/ui";
import { Trophy } from "@/components/icons";
import { fmt } from "@/lib/mock";
import { fetchTournament, type Tournament } from "@/lib/api";

// GET /v1/tournaments/{id} — tournament detail (public).
export default function TournamentsPage() {
  const [id, setId] = useState("");
  const [data, setData] = useState<Tournament | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function lookup() {
    const q = id.trim();
    if (!q) return;
    setBusy(true);
    setErr(null);
    setData(null);
    const t = await fetchTournament(q);
    if (t) setData(t);
    else setErr(`No tournament found for "${q}".`);
    setBusy(false);
  }

  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-container px-6 py-10">
        <SectionLabel className="mb-3 text-secondary">TOURNAMENTS</SectionLabel>
        <h1 className="font-display text-4xl font-bold tracking-[-0.5px]">Tournament lookup</h1>
        <p className="mt-2 max-w-xl text-ink-dim">
          Sponsored brackets with carryover prize pools. Enter a tournament ID to
          view its status, pool and winner.
        </p>

        <div className="mt-6 flex max-w-xl gap-3">
          <input
            className="input flex-1"
            placeholder="tr_… tournament id"
            value={id}
            onChange={(e) => setId(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && lookup()}
          />
          <button onClick={lookup} disabled={busy || !id.trim()} className="btn-primary disabled:opacity-50">
            {busy ? "Looking…" : "Look up"}
          </button>
        </div>
        {err && <p className="mt-3 font-mono text-[12px] text-status-error">{err}</p>}

        {data && (
          <Panel glass className="mt-8 p-7">
            <div className="flex items-start justify-between">
              <div>
                <SectionLabel className="text-secondary">{data.sponsor ?? "OPEN"} · {data.tournament_id}</SectionLabel>
                <h2 className="mt-2 font-display text-2xl font-semibold">{data.name}</h2>
              </div>
              <span className="text-secondary"><Trophy width={26} height={26} /></span>
            </div>
            <div className="mt-6 grid grid-cols-2 gap-4 border-t border-border-soft pt-5 sm:grid-cols-4">
              <Stat label="PRIZE POOL" value={fmt(data.prize_pool)} tone="amber" />
              <Stat label="ENTRIES" value={fmt(data.entries)} tone="teal" />
              <Stat label="STATUS" value={data.status.toUpperCase()} />
              <Stat label="WINNER" value={data.winner || "—"} tone="blue" />
            </div>
            {data.status !== "finalized" && (
              <Pill tone="teal" dot className="mt-6">REGISTRATION OPEN</Pill>
            )}
          </Panel>
        )}
      </div>
      <Footer />
    </div>
  );
}
