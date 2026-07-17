import TraceWorkbench from "@/components/trace/trace-workbench";
import { fetchQuery, type EventRow, type SpanNode, type TraceSummary } from "@/lib/pyyol-lens-api";

export default async function TraceDetailPage({
  params,
}: {
  params: Promise<{ traceId: string }>;
}) {
  const { traceId } = await params;
  const [trace, tree, events] = await Promise.all([
    fetchQuery<TraceSummary>(`/v1/traces/${traceId}`),
    fetchQuery<SpanNode[]>(`/v1/traces/${traceId}/tree`),
    fetchQuery<EventRow[]>(`/v1/traces/${traceId}/events`),
  ]);

  if (!trace) {
    return <div className="page"><div className="panel">Trace not found.</div></div>;
  }

  return (
    <TraceWorkbench trace={trace} tree={tree ?? []} events={events ?? []} />
  );
}
