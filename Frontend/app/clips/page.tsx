import Link from "next/link";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, Pill, SectionLabel } from "@/components/ui";
import { fmt } from "@/lib/mock";
import { fetchClips } from "@/lib/api";

// GET /v1/clips/trending — auto-generated highlight clips (public).
export default async function ClipsPage() {
  const clips = await fetchClips();
  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-container px-6 py-10">
        <div className="flex flex-wrap items-end justify-between gap-4">
          <div>
            <SectionLabel className="mb-3 text-secondary">HIGHLIGHTS</SectionLabel>
            <h1 className="font-display text-4xl font-bold tracking-[-0.5px]">
              Trending clips
            </h1>
            <p className="mt-2 text-ink-dim">
              Dramatic moments the engine clipped automatically — carryover steals,
              perfect reads, last-card upsets.
            </p>
          </div>
          <Pill tone="amber" dot>AUTO-CLIPPED</Pill>
        </div>

        {clips.length === 0 ? (
          <Panel className="mt-8 p-10 text-center">
            <p className="font-mono text-sm text-ink-dim">
              No clips yet. Highlights appear here as live matches produce dramatic rounds.
            </p>
          </Panel>
        ) : (
          <div className="mt-8 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {clips.map((c) => (
              <Panel key={c.clip_id} className="flex flex-col overflow-hidden">
                <div className="relative aspect-video bg-bg-deep bg-redact">
                  <span className="absolute left-3 top-3">
                    <Pill tone="red" dot>{c.trigger.replace(/_/g, " ").toUpperCase()}</Pill>
                  </span>
                  <span className="absolute bottom-3 right-3 font-mono text-[11px] text-ink-faint">
                    ROUND {c.round_seq}
                  </span>
                </div>
                <div className="flex flex-1 flex-col p-5">
                  <div className="font-mono text-sm text-ink-primary">Match {c.match_id}</div>
                  <div className="mt-1 font-mono text-[11px] text-ink-faint">
                    {new Date(c.created_at).toLocaleString()}
                  </div>
                  <div className="mt-4 flex items-center justify-between border-t border-border-soft pt-3">
                    <span className="label-caps">
                      ◎ {fmt(c.share_count)} shares
                    </span>
                    <Link
                      href={c.asset_url || `/spectate`}
                      className="font-mono text-[11px] uppercase tracking-caps text-primary hover:underline"
                    >
                      Watch clip →
                    </Link>
                  </div>
                </div>
              </Panel>
            ))}
          </div>
        )}
      </div>
      <Footer />
    </div>
  );
}
