import { Unavailable } from "@/components/Unavailable";
import { fetchQueryResult } from "@/lib/pyyol-lens-api";

type UsageRow = {
  project_id: string;
  environment: string;
  provider: string;
  model: string;
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
};

export default async function TokenUsagePage() {
  const result = await fetchQueryResult<UsageRow[]>("/v1/usage/summary");
  const rows = result.ok ? result.data : [];
  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Token Usage</h1>
          <p>Provider- and model-level token visibility across projects and environments.</p>
        </div>
      </section>
      {!result.ok ? <Unavailable title="Token usage unavailable" /> : null}
      <section className="panel">
        <p className="table-count">{rows.length.toLocaleString()} usage rows</p>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Project</th>
                <th>Environment</th>
                <th>Model</th>
                <th>Prompt</th>
                <th>Completion</th>
                <th>Total</th>
              </tr>
            </thead>
            <tbody>
              {rows.length ? (
                rows.map((row, index) => (
                  <tr key={`${row.project_id}-${row.model}-${index}`}>
                    <td>{row.project_id || "-"}</td>
                    <td>{row.environment || "-"}</td>
                    <td>{row.provider}/{row.model}</td>
                    <td>{row.prompt_tokens.toLocaleString()}</td>
                    <td>{row.completion_tokens.toLocaleString()}</td>
                    <td>{row.total_tokens.toLocaleString()}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={6} className="empty-state">
                    {result.ok ? "No token usage yet." : "Waiting for the query API."}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
