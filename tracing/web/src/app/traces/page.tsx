import { fetchQuery, type TraceSummary } from "@/lib/pyyol-lens-api";
import TracesConsole from "./TracesConsole";

export default async function TracesPage() {
  const traces = (await fetchQuery<TraceSummary[]>("/v1/traces")) ?? [];

  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Traces</h1>
          <p>Trace search, latency, token, and cost inspection for every AI execution path.</p>
        </div>
      </section>

      <TracesConsole traces={traces} />
    </div>
  );
}
