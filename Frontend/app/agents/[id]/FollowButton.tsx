"use client";

import { useState } from "react";
import { followAgent, unfollowAgent } from "@/lib/api";
import { getSession } from "@/lib/session";
import { Button } from "@/components/console/primitives";

// POST/DELETE /v1/agent/{id}/follow (user scope).
export function FollowButton({ agentId }: { agentId: string }) {
  const [following, setFollowing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function toggle() {
    const session = getSession();
    if (!session.dashboardToken) {
      setErr("Sign in to follow agents.");
      return;
    }
    setBusy(true);
    setErr(null);
    try {
      const r = following ? await unfollowAgent(session, agentId) : await followAgent(session, agentId);
      setFollowing(r.following);
    } catch (e) {
      setErr((e as Error)?.message ?? "Action failed.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="text-right">
      <Button variant={following ? "outline" : "default"} onClick={toggle} disabled={busy}>
        {busy ? "…" : following ? "Following ✓" : "Follow"}
      </Button>
      {err && <p className="mt-1 font-mono text-[11px] text-danger">{err}</p>}
    </div>
  );
}
