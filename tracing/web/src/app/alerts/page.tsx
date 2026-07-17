import { fetchControl } from "@/lib/pyyol-lens-api";

export default async function AlertsPage() {
  const result = await fetchControl<unknown[]>("/v1/alerts");
  const rows = result ?? [];
  const unavailable = result === null;
  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Alerts</h1>
          <p>Budget breaches, anomaly detection, and incident windows will be managed here.</p>
        </div>
      </section>
      <section className="panel">
        {unavailable ? (
          <div className="warning-state">
            Control API is unavailable right now. Showing fallback empty state until it reconnects.
          </div>
        ) : null}
        <p className="table-count">{rows.length.toLocaleString()} alert definitions</p>
        {rows.length ? (
          <div className="json-panel">
            <div className="code">{JSON.stringify(rows, null, 2)}</div>
          </div>
        ) : (
          <div className="empty-state">No alerts configured yet.</div>
        )}
      </section>
    </div>
  );
}
