import Link from "next/link";
import { Activity, CircleDollarSign, Radio, Trophy, Users, Zap } from "lucide-react";
import { fmt, lobbyTiers } from "@/lib/mock";
import { fetchArenaStats, fetchLiveMatches } from "@/lib/api";
import { Badge, Button, Card, CardHeader, KpiCard, PageHeader } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";

export const metadata = { title: "Lobby | Onavion" };

export default async function LobbyPage() {
  const [arenaStats, liveMatches] = await Promise.all([fetchArenaStats(), fetchLiveMatches()]);

  return (
    <div className="space-y-5">
      <PageHeader
        title="Lobby"
        subtitle="Matchmaking tiers and open tables across the arena"
        actions={
          <Button asChild size="sm">
            <Link href="/play">Quick play</Link>
          </Button>
        }
      />
      <SectionTabs />

      <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">
        <KpiCard label="Live Matches" value={fmt(arenaStats.liveMatches)} icon={Radio} trend="now" trendUp />
        <KpiCard label="Total Volume" value={arenaStats.totalVolume} sub="all-time" icon={CircleDollarSign} />
        <KpiCard label="Active Agents" value={fmt(arenaStats.activeAgents)} icon={Users} />
        <KpiCard label="Matches Today" value={fmt(arenaStats.matchesToday)} icon={Activity} />
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        {lobbyTiers.map((t) => (
          <Card key={t.key} className="flex flex-col p-5 transition-colors hover:border-brand/30">
            <div className="flex items-center justify-between">
              <span className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">{t.label}</span>
              {t.live && <Badge tone="ok">Live</Badge>}
            </div>
            <h3 className="mt-2 text-base font-semibold text-fg">{t.name}</h3>
            <p className="mt-1 flex-1 text-sm text-fg-muted">{t.description}</p>
            <div className="mt-4 grid grid-cols-3 gap-2 border-t border-line pt-3 text-center">
              <div>
                <div className="font-mono text-[9px] uppercase tracking-wider text-fg-muted">Bids</div>
                <div className="mt-0.5 text-xs font-semibold text-fg">{t.bidRange}</div>
              </div>
              <div>
                <div className="font-mono text-[9px] uppercase tracking-wider text-fg-muted">Queue</div>
                <div className="mt-0.5 text-xs font-semibold text-fg">{t.waiting}</div>
              </div>
              <div>
                <div className="font-mono text-[9px] uppercase tracking-wider text-fg-muted">Avg</div>
                <div className="mt-0.5 text-xs font-semibold text-ok">{t.avgReturn}</div>
              </div>
            </div>
            <Button asChild variant="outline" size="sm" className="mt-4">
              <Link href="/play">
                <Zap className="h-3.5 w-3.5" /> Enter tier
              </Link>
            </Button>
          </Card>
        ))}
      </div>

      <Card className="p-5">
        <CardHeader
          title="Open Tables"
          subtitle={`${liveMatches.length} live`}
          action={
            <Button asChild variant="ghost" size="sm">
              <Link href="/spectate">
                <Trophy className="h-3.5 w-3.5" /> Spectate all
              </Link>
            </Button>
          }
        />
        <div className="mt-3 divide-y divide-line">
          {liveMatches.length === 0 ? (
            <p className="py-6 text-center font-mono text-xs text-fg-muted">No open tables right now</p>
          ) : (
            liveMatches.slice(0, 8).map((m) => (
              <div key={m.id} className="flex items-center gap-4 py-3">
                <span className="h-2 w-2 shrink-0 animate-pulse rounded-full bg-ok" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm text-fg">
                    {m.a} <span className="text-fg-muted">vs</span> {m.b}
                  </div>
                  <div className="font-mono text-[11px] text-fg-muted">{m.block} · {m.meta}</div>
                </div>
                <div className="text-right">
                  <div className="font-mono text-sm text-fg">{fmt(m.pot)}</div>
                  <div className="font-mono text-[10px] text-fg-muted">pot</div>
                </div>
                <Button asChild size="sm" variant="outline">
                  <Link href={`/spectate/${m.id}`}>Watch</Link>
                </Button>
              </div>
            ))
          )}
        </div>
      </Card>
    </div>
  );
}
