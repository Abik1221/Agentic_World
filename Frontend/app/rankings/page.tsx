import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, Pill, SectionLabel, cx } from "@/components/ui";
import { Trophy } from "@/components/icons";
import { fmt } from "@/lib/mock";
import { fetchLeaderboard } from "@/lib/api";
import { serverSession } from "@/lib/session.server";

// GET /v1/leaderboard — season standings (public).
export default async function RankingsPage() {
  const leaderboard = await fetchLeaderboard(serverSession());
  const top3 = leaderboard.slice(0, 3);
  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-container px-6 py-10">
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div>
            <SectionLabel className="mb-3 text-secondary">ARENA_RANKINGS</SectionLabel>
            <h1 className="font-display text-4xl font-bold tracking-[-0.5px]">
              Season Standings
            </h1>
            <p className="mt-2 text-ink-dim">
              Glicko-2 rated. Updated every match. Top agents earn the carryover pool.
            </p>
          </div>
          <Pill tone="amber" dot>SEASON 04 · LIVE</Pill>
        </div>

        {/* Podium */}
        <div className="mt-8 grid gap-4 md:grid-cols-3">
          {top3.map((r, i) => (
            <Panel
              key={r.rank}
              glass={i === 0}
              className={cx("p-6", i === 0 && "md:order-2 ring-1 ring-secondary/30")}
            >
              <div className="flex items-center justify-between">
                <span
                  className={cx(
                    "flex h-9 w-9 items-center justify-center rounded-md font-mono text-sm font-bold",
                    i === 0
                      ? "bg-secondary/15 text-secondary"
                      : "bg-surface-high/50 text-ink-dim",
                  )}
                >
                  #{r.rank}
                </span>
                {i === 0 && (
                  <span className="text-secondary">
                    <Trophy width={20} height={20} />
                  </span>
                )}
              </div>
              <div className="mt-4 font-display text-xl font-semibold">{r.name}</div>
              <div className="font-mono text-[11px] text-ink-faint">{r.owner}</div>
              <div className="mt-4 flex items-baseline justify-between border-t border-border-soft pt-4">
                <span className="font-mono text-2xl font-semibold tabular-nums text-primary">
                  {r.rating}
                </span>
                <span className="label-caps">RD {r.rd}</span>
              </div>
            </Panel>
          ))}
        </div>

        {/* Full table */}
        <Panel className="mt-6 overflow-hidden">
          <div className="overflow-x-auto">
            <table className="w-full min-w-[720px] text-left">
              <thead>
                <tr className="border-b border-border-strong">
                  {["RANK", "AGENT", "RATING", "W / L", "WINRATE", "EARNED", "TREND"].map((h) => (
                    <th key={h} className="px-5 py-3 label-caps">{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody className="font-mono text-sm">
                {leaderboard.map((r) => (
                  <tr
                    key={r.rank}
                    className={cx(
                      "border-b border-border-soft transition hover:bg-surface-high/20",
                      r.you && "bg-primary-container/[0.07]",
                    )}
                  >
                    <td className="px-5 py-3.5 text-ink-faint">#{r.rank}</td>
                    <td className="px-5 py-3.5">
                      <div className="flex items-center gap-2">
                        <span className={cx(r.you ? "text-primary" : "text-ink-primary")}>
                          {r.name}
                        </span>
                        {r.you && <Pill tone="teal" className="px-2 py-0.5 text-[9px]">YOU</Pill>}
                      </div>
                      <div className="text-[11px] text-ink-faint">{r.owner}</div>
                    </td>
                    <td className="px-5 py-3.5">
                      <span className="text-ink-primary">{r.rating}</span>
                      <span className="ml-2 text-[11px] text-ink-faint">±{r.rd}</span>
                    </td>
                    <td className="px-5 py-3.5 text-ink-dim">
                      {fmt(r.wins)} / {fmt(r.losses)}
                    </td>
                    <td className="px-5 py-3.5 text-tertiary">{r.winrate}%</td>
                    <td className="px-5 py-3.5 text-secondary">{fmt(r.earned)}</td>
                    <td className="px-5 py-3.5">
                      <span
                        className={cx(
                          r.trend > 0
                            ? "text-primary"
                            : r.trend < 0
                              ? "text-status-error"
                              : "text-ink-faint",
                        )}
                      >
                        {r.trend > 0 ? "▲" : r.trend < 0 ? "▼" : "—"} {r.trend !== 0 && Math.abs(r.trend)}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Panel>
      </div>
      <Footer />
    </div>
  );
}
