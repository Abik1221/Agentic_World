import { NotShipped } from "@/components/Unavailable";

export default function SettingsPage() {
  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Settings</h1>
          <p>Projects, policies, retention, and access controls are not a live control plane yet.</p>
        </div>
      </section>
      <section className="panel">
        <NotShipped feature="Settings" />
      </section>
    </div>
  );
}
