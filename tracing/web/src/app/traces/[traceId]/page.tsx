import TraceWorkbench from "@/components/trace/trace-workbench";
import { Unavailable } from "@/components/Unavailable";
import { fetchQueryResult, type EventRow, type SpanNode, type TraceSummary } from "@/lib/pyyol-lens-api";

export default async function TraceDetailPage({
  params,
}: {
  params: Promise<{ traceId: string }>;
}) {
  const { traceId } = await params;
  const [traceRes, treeRes, eventsRes] = await Promise.all([
    fetchQueryResult<TraceSummary>(`/v1/traces/${traceId}`),
    fetchQueryResult<SpanNode[]>(`/v1/traces/${traceId}/tree`),
    fetchQueryResult<EventRow[]>(`/v1/traces/${traceId}/events`),
  ]);

  if (!traceRes.ok) {
    if (traceRes.reason === "not_found") {
      return <div className="page"><div className="panel">Trace not found.</div></div>;
    }
    return (
      <div className="page">
        <Unavailable title="Trace detail unavailable" />
      </div>
    );
  }

  return (
    <TraceWorkbench
      trace={traceRes.data}
      tree={treeRes.ok ? treeRes.data : []}
      events={eventsRes.ok ? eventsRes.data : []}
    />
  );
}
