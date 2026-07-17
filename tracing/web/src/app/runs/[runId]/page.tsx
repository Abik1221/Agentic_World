import TraceWorkbench from "@/components/trace/trace-workbench";
import { fetchQuery, type EventRow, type SpanNode, type TraceSummary } from "@/lib/pyyol-lens-api";

export default async function RunDetailPage({
  params,
}: {
  params: Promise<{ runId: string }>;
}) {
  const { runId } = await params;
  const [trace, tree, events] = await Promise.all([
    fetchQuery<TraceSummary>(`/v1/traces/${runId}`),
    fetchQuery<SpanNode[]>(`/v1/traces/${runId}/tree`),
    fetchQuery<EventRow[]>(`/v1/traces/${runId}/events`),
  ]);

  if (!trace) {
    return <div className="page"><div className="panel">Run not found.</div></div>;
  }

  return <TraceWorkbench trace={trace} tree={tree ?? []} events={events ?? []} />;
}
