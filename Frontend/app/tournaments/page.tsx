"use client";

import * as React from "react";
import { Search, Trophy, Users } from "lucide-react";
import { fmt } from "@/lib/mock";
import { fetchTournament, type Tournament } from "@/lib/api";
import { cn } from "@/lib/cn";
import { Button, Card, CardHeader, KpiCard, PageHeader, StatusBadge } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";

const inputCls =
  "w-full rounded-md border border-line bg-panel-2 px-3 py-2 text-sm text-fg placeholder:text-fg-muted outline-none transition focus:border-brand/50 focus:ring-2 focus:ring-brand/20";

export default function TournamentsPage() {
  const [id, setId] = React.useState("");
  const [data, setData] = React.useState<Tournament | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState<string | null>(null);

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
    <div className="space-y-5">
      <PageHeader title="Tournaments" subtitle="Funded freerolls · look up a tournament by ID" />
      <SectionTabs />

      <Card className="p-5">
        <CardHeader title="Lookup" subtitle="Enter a tournament ID" />
        <div className="mt-4 flex gap-2">
          <input
            className={inputCls}
            placeholder="trn_…"
            value={id}
            onChange={(e) => setId(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && lookup()}
          />
          <Button onClick={lookup} disabled={busy}>
            <Search className="h-4 w-4" />
            {busy ? "Searching…" : "Look up"}
          </Button>
        </div>
        {err && <p className={cn("mt-3 font-mono text-[12px] text-danger")}>✕ {err}</p>}
      </Card>

      {data && (
        <div className="space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-line bg-panel p-5">
            <div className="flex items-center gap-3">
              <div className="flex h-11 w-11 items-center justify-center rounded-full border border-warn/40 bg-warn/10">
                <Trophy className="h-5 w-5 text-warn" />
              </div>
              <div>
                <p className="text-base font-semibold text-fg">{data.name}</p>
                <p className="font-mono text-[11px] text-fg-muted">
                  {data.tournament_id}
                  {data.sponsor ? ` · sponsored by ${data.sponsor}` : ""}
                </p>
              </div>
            </div>
            <StatusBadge status={data.status} />
          </div>

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <KpiCard label="Prize Pool" value={fmt(data.prize_pool)} sub="coins" icon={Trophy} />
            <KpiCard label="Entries" value={data.entries} icon={Users} />
            <KpiCard label="Winner" value={data.winner ?? "—"} sub={data.winner ? "champion" : "in progress"} icon={Trophy} />
          </div>
        </div>
      )}
    </div>
  );
}
