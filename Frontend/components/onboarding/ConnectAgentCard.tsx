"use client";

import * as React from "react";
import { Card, CardHeader } from "@/components/console/primitives";
import { ConnectAgentGuide } from "@/components/onboarding/ConnectAgentGuide";
import { fetchAgentStatus, type AgentConnStatus } from "@/lib/api";
import { getSession } from "@/lib/session";

// Dashboard "connect your agent" card. Live: it polls /v1/agent/status — when the
// agent is connected over the socket it shows a green Online badge (SDK + games +
// last-seen); when offline it shows the CLI quickstart to bring it online.
export function ConnectAgentCard() {
  const [status, setStatus] = React.useState<AgentConnStatus | null>(null);

  React.useEffect(() => {
    const s = getSession();
    if (!s.agentId) {
      setStatus({ online: false });
      return;
    }
    let alive = true;
    const load = () => fetchAgentStatus(s, s.agentId!).then((r) => alive && setStatus(r)).catch(() => {});
    load();
    const iv = setInterval(load, 10000);
    return () => {
      alive = false;
      clearInterval(iv);
    };
  }, []);

  const online = status?.online === true;

  return (
    <Card className="p-5">
      <div className="flex items-center justify-between">
        <CardHeader
          title="Your agent"
          subtitle={online ? "Connected and ready for matches" : "Run it locally with the Pyyol CLI to bring it online"}
        />
        <StatusPill loading={status === null} online={online} />
      </div>

      {online ? (
        <div className="mt-3 flex flex-wrap gap-x-6 gap-y-2 font-mono text-[11px] text-fg-muted">
          <span>SDK <span className="text-fg">{status?.sdk_version || "?"}</span></span>
          <span>Games <span className="text-fg">{(status?.games || []).join(", ") || "—"}</span></span>
          {status?.last_seen && <span>Last seen <span className="text-fg">{fmtSeen(status.last_seen)}</span></span>}
        </div>
      ) : (
        <div className="mt-4">
          <ConnectAgentGuide compact />
        </div>
      )}
    </Card>
  );
}

function StatusPill({ online, loading }: { online: boolean; loading: boolean }) {
  if (loading) {
    return <span className="font-mono text-[11px] text-fg-muted">checking…</span>;
  }
  return (
    <span
      className={
        "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 font-mono text-[10px] uppercase tracking-widest " +
        (online ? "border-ok/40 bg-ok/10 text-ok" : "border-line bg-panel-2/60 text-fg-muted")
      }
    >
      <span className={"h-1.5 w-1.5 rounded-full " + (online ? "bg-ok live-dot" : "bg-fg-muted")} />
      {online ? "Online" : "Offline"}
    </span>
  );
}

function fmtSeen(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const s = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  return `${Math.floor(s / 3600)}h ago`;
}
