import { fetchControl } from "@/lib/pyyol-lens-api";

export default async function SettingsPage() {
  const [projects, policies] = await Promise.all([
    fetchControl<unknown[]>("/v1/projects"),
    fetchControl<unknown[]>("/v1/policies"),
  ]);
  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Settings</h1>
          <p>Projects, policies, retention, and access controls converge in the control plane.</p>
        </div>
      </section>
      <section className="grid-two">
        <div className="panel">
          <h2>Projects</h2>
          <div className="code">{JSON.stringify(projects ?? [], null, 2)}</div>
        </div>
        <div className="panel">
          <h2>Policies</h2>
          <div className="code">{JSON.stringify(policies ?? [], null, 2)}</div>
        </div>
      </section>
    </div>
  );
}
