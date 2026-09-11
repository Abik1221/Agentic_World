import { NotShipped } from "@/components/Unavailable";

export default function AlertsPage() {
  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Alerts</h1>
          <p>Budget breaches and anomaly windows will land here once the control plane ships them.</p>
        </div>
      </section>
      <section className="panel">
        <NotShipped feature="Alerts" />
      </section>
    </div>
  );
}
