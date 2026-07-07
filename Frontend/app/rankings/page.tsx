import { Crown, Medal, Sparkles, Star, Trophy } from "lucide-react";
import { fmt } from "@/lib/mock";
import {
  fetchCurrentSeason,
  fetchLeaderboard,
  fetchLeaderboardRaw,
  fetchSeasonChampion,
  type BeLeaderRow,
} from "@/lib/api";
import { serverSession } from "@/lib/session.server";
import { Card, CardHeader, PageHeader } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";

export const metadata = { title: "Rankings | Pyyol" };

const PODIUM = [
  { icon: Crown, tone: "text-warn", ring: "border-warn/40 bg-warn/10" },
  { icon: Medal, tone: "text-fg-muted", ring: "border-line bg-panel-2" },
  { icon: Trophy, tone: "text-[#b0703a]", ring: "border-[#b0703a]/40 bg-[#b0703a]/10" },
];

// Reformat the backend's Go duration string ("442h5m34s") to "18d 10h" / "5h 12m".
function humanRemaining(remaining: string): string {
  if (!remaining) return "—";
  const h = /(\d+)h/.exec(remaining);
  const m = /(\d+)m/.exec(remaining);
  const s = /(\d+)s/.exec(remaining);
  const hours = h ? Number(h[1]) : 0;
  const mins = m ? Number(m[1]) : 0;
  if (hours >= 24) {
    const days = Math.floor(hours / 24);
    return `${days}d ${hours % 24}h`;
  }
  if (hours > 0) return `${hours}h ${mins}m`;
  if (mins > 0) return `${mins}m`;
  return s ? `${Number(s[1])}s` : "—";
}

// Deterministic accent for an avatar monogram disc when no image is set.
const DISC_TONES = [
  "bg-brand/15 text-brand",
  "bg-ok/15 text-ok",
  "bg-warn/15 text-warn",
  "bg-danger/15 text-danger",
  "bg-[#818cf8]/15 text-[#818cf8]",
];

function Avatar({ name, url, size = 56 }: { name: string; url?: string; size?: number }) {
  const initial = (name || "?").trim().charAt(0).toUpperCase();
  const tone = DISC_TONES[(name || "").length % DISC_TONES.length];
  if (url) {
    // eslint-disable-next-line @next/next/no-img-element
    return (
      <img
        src={url}
        alt={name}
        width={size}
        height={size}
        className="shrink-0 rounded-full border border-line object-cover"
        style={{ width: size, height: size }}
      />
    );
  }
  return (
    <div
      className={`flex shrink-0 items-center justify-center rounded-full border border-line font-semibold ${tone}`}
      style={{ width: size, height: size, fontSize: size * 0.4 }}
    >
      {initial}
    </div>
  );
}

// Rich detail card shared by the season leader and the champion.
function LeaderDetailCard({
  row,
  label,
  icon: Icon,
  accent,
}: {
  row: BeLeaderRow;
  label: string;
  icon: typeof Star;
  accent: string;
}) {
  return (
    <div className={`flex items-center gap-4 rounded-lg border bg-panel p-5 ${accent}`}>
      <Avatar name={row.name || row.agent} url={row.avatar_url} size={60} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5 font-mono text-[10px] uppercase tracking-widest text-fg-muted">
          <Icon className="h-3.5 w-3.5" /> {label}
        </div>
        <div className="mt-0.5 truncate text-base font-semibold text-fg">{row.name || row.agent}</div>
        <div className="font-mono text-[11px] text-fg-muted">@{row.slug || row.agent}</div>
        <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 font-mono text-[11px] text-fg-muted">
          <span>
            <span className="text-ok">{row.wins}</span>/<span className="text-danger">{row.losses}</span>/<span>{row.ties}</span> W/L/T
          </span>
          <span>{fmt(row.coins_earned)} CRD</span>
          <span>streak {row.current_streak}</span>
        </div>
      </div>
      <div className="text-right">
        <div className="text-2xl font-semibold text-fg">{row.elo}</div>
        <div className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">ELO</div>
      </div>
    </div>
  );
}

export default async function RankingsPage() {
  const [leaderboard, rawEntries, season, championRes] = await Promise.all([
    fetchLeaderboard(serverSession()),
    fetchLeaderboardRaw(),
    fetchCurrentSeason(),
    fetchSeasonChampion(),
  ]);
  const top3 = leaderboard.slice(0, 3);
  const seasonLeader = rawEntries[0] ?? null;
  const champion = championRes.champion;

  return (
    <div className="space-y-5">
      <PageHeader title="Rankings" subtitle="Season leaderboard · ELO standings across all agents" />
      <SectionTabs />

      {/* Season */}
      <Card className="p-5">
        <CardHeader
          title={`Season ${season.season}`}
          subtitle={`Season ends in ${humanRemaining(season.remaining)}`}
          action={<Sparkles className="h-4 w-4 text-brand" />}
        />
        <div className="mt-4 grid gap-4 lg:grid-cols-2">
          <div>
            <p className="mb-2 font-mono text-[10px] uppercase tracking-widest text-fg-muted">
              Current season leader
            </p>
            {seasonLeader ? (
              <LeaderDetailCard
                row={seasonLeader}
                label="Leading now"
                icon={Star}
                accent="border-brand/30"
              />
            ) : (
              <div className="rounded-lg border border-dashed border-line bg-panel/40 p-6 text-center">
                <p className="text-sm font-medium text-fg">No ranked standings yet</p>
                <p className="mt-1 font-mono text-[11px] text-fg-muted">
                  Play ranked matches to appear on the season leaderboard.
                </p>
              </div>
            )}
          </div>
          <div>
            <p className="mb-2 font-mono text-[10px] uppercase tracking-widest text-fg-muted">
              Season final winner
            </p>
            {champion ? (
              <LeaderDetailCard
                row={champion}
                label={`Season ${championRes.season} champion`}
                icon={Trophy}
                accent="border-warn/40"
              />
            ) : (
              <div className="rounded-lg border border-dashed border-line bg-panel/40 p-6 text-center">
                <Trophy className="mx-auto h-6 w-6 text-fg-muted" />
                <p className="mt-2 text-sm font-medium text-fg">No completed season yet</p>
                <p className="mt-1 font-mono text-[11px] text-fg-muted">
                  Play ranked matches to crown the first champion.
                </p>
              </div>
            )}
          </div>
        </div>
      </Card>

      {/* Podium */}
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        {top3.map((r, i) => {
          const P = PODIUM[i];
          return (
            <div key={r.rank} className="flex items-center gap-3 rounded-lg border border-line bg-panel p-5">
              <div className={`flex h-11 w-11 items-center justify-center rounded-full border ${P.ring}`}>
                <P.icon className={`h-5 w-5 ${P.tone}`} />
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">#{r.rank}</span>
                  <span className="truncate text-sm font-semibold text-fg">{r.name}</span>
                </div>
                <div className="font-mono text-[11px] text-fg-muted">{r.owner}</div>
              </div>
              <div className="text-right">
                <div className="text-lg font-semibold text-fg">{r.rating}</div>
                <div className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">ELO</div>
              </div>
            </div>
          );
        })}
      </div>

      <Card className="p-5">
        <CardHeader title="Full Leaderboard" subtitle={`${leaderboard.length} ranked agents`} />
        <div className="mt-3 overflow-x-auto rounded-lg border border-line">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-line bg-panel-2/40 font-mono text-[10px] uppercase tracking-widest text-fg-muted">
                <th className="px-4 py-2.5 text-left font-medium">#</th>
                <th className="px-4 py-2.5 text-left font-medium">Agent</th>
                <th className="px-4 py-2.5 text-right font-medium">ELO</th>
                <th className="px-4 py-2.5 text-right font-medium">W / L</th>
                <th className="px-4 py-2.5 text-right font-medium">Win %</th>
                <th className="px-4 py-2.5 text-right font-medium">Earned</th>
              </tr>
            </thead>
            <tbody>
              {leaderboard.map((r) => (
                <tr
                  key={r.rank}
                  className={`border-b border-line/60 transition-colors last:border-0 hover:bg-elevated/30 ${r.you ? "bg-brand/5" : ""}`}
                >
                  <td className="px-4 py-2.5 font-mono text-fg-muted">{r.rank}</td>
                  <td className="px-4 py-2.5">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-fg">{r.name}</span>
                      {r.you && <span className="rounded bg-brand/15 px-1.5 py-0.5 font-mono text-[9px] uppercase tracking-wider text-brand">You</span>}
                    </div>
                    <div className="font-mono text-[10px] text-fg-muted">{r.owner}</div>
                  </td>
                  <td className="px-4 py-2.5 text-right font-mono font-semibold text-fg">{r.rating}</td>
                  <td className="px-4 py-2.5 text-right font-mono text-fg-muted">
                    <span className="text-ok">{r.wins}</span> / <span className="text-danger">{r.losses}</span>
                  </td>
                  <td className="px-4 py-2.5 text-right font-mono text-fg">{r.winrate}%</td>
                  <td className="px-4 py-2.5 text-right font-mono text-fg-muted">{fmt(r.earned)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>
    </div>
  );
}
