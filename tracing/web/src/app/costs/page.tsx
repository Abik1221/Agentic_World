import { Unavailable } from "@/components/Unavailable";
import { fetchQueryResult } from "@/lib/pyyol-lens-api";

type CostRow = {
  project_id: string;
  environment: string;
  provider: string;
  model: string;
  estimated_cost: number;
  reconciled_cost: number;
  currency: string;
};

export default async function CostsPage() {
  const result = await fetchQueryResult<CostRow[]>("/v1/costs/summary");
  const rows = result.ok ? result.data : [];
  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Costs</h1>
          <p>Estimated and reconciled model spend by project, environment, and model.</p>
        </div>
      </section>
      {!result.ok ? <Unavailable title="Cost data unavailable" /> : (
      <section className="panel">
        <p className="table-count">{rows.length.toLocaleString()} cost rows</p>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Project</th>
                <th>Environment</th>
                <th>Model</th>
                <th>Estimated</th>
                <th>Reconciled</th>
              </tr>
            </thead>
            <tbody>
              {rows.length ? (
                rows.map((row, index) => (
                  <tr key={`${row.project_id}-${row.model}-${index}`}>
                    <td>{row.project_id || "-"}</td>
                    <td>{row.environment || "-"}</td>
                    <td>{row.provider}/{row.model}</td>
                    <td>{row.currency || "USD"} {row.estimated_cost.toFixed(4)}</td>
                    <td>{row.currency || "USD"} {row.reconciled_cost.toFixed(4)}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={5} className="empty-state">
                    No cost data yet.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
      )}
    </div>
  );
}
