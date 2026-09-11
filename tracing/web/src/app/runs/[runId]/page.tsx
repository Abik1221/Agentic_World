import TraceWorkbench from "@/components/trace/trace-workbench";
import { Unavailable } from "@/components/Unavailable";
import { fetchQueryResult, type EventRow, type SpanNode, type TraceSummary } from "@/lib/pyyol-lens-api";

export default async function RunDetailPage({
  params,
}: {
  params: Promise<{ runId: string }>;
}) {
  const { runId } = await params;
  const [traceRes, treeRes, eventsRes] = await Promise.all([
    fetchQueryResult<TraceSummary>(`/v1/traces/${runId}`),
    fetchQueryResult<SpanNode[]>(`/v1/traces/${runId}/tree`),
    fetchQueryResult<EventRow[]>(`/v1/traces/${runId}/events`),
  ]);

  if (!traceRes.ok) {
    if (traceRes.reason === "not_found") {
      return <div className="page"><div className="panel">Run not found.</div></div>;
    }
    return (
      <div className="page">
        <Unavailable title="Run detail unavailable" />
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
