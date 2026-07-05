import Link from "next/link";
import { Film, Share2 } from "lucide-react";
import { fmt } from "@/lib/mock";
import { fetchClips } from "@/lib/api";
import { Card, EmptyState, PageHeader } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";

export const metadata = { title: "Clips | Onavion" };

export default async function ClipsPage() {
  const clips = await fetchClips();

  return (
    <div className="space-y-5">
      <PageHeader title="Clips" subtitle="Auto-generated highlight reels from dramatic match moments" />
      <SectionTabs />

      {clips.length === 0 ? (
        <EmptyState icon={Film} title="No clips yet" hint="Highlights are generated automatically when matches hit dramatic moments." />
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {clips.map((c) => (
            <Card key={c.clip_id} className="overflow-hidden transition-colors hover:border-brand/30">
              <div className="relative flex aspect-video items-center justify-center bg-panel-2">
                <Film className="h-7 w-7 text-fg-muted" />
                <span className="absolute left-2 top-2 rounded bg-brand/15 px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-wider text-brand">
                  {c.trigger.replace(/_/g, " ")}
                </span>
              </div>
              <div className="p-4">
                <div className="flex items-center justify-between">
                  <span className="font-mono text-[11px] text-fg-muted">{c.match_id.slice(0, 12)}</span>
                  <span className="flex items-center gap-1 font-mono text-[11px] text-fg-muted">
                    <Share2 className="h-3 w-3" /> {fmt(c.share_count)}
                  </span>
                </div>
                <div className="mt-1 flex items-center justify-between">
                  <span className="text-sm font-medium text-fg">Round {c.round_seq}</span>
                  <Link href={c.asset_url} className="text-xs text-brand hover:underline">
                    Watch →
                  </Link>
                </div>
              </div>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
