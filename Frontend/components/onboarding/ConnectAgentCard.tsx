"use client";

import * as React from "react";
import { Card, CardHeader } from "@/components/console/primitives";
import { ConnectAgentGuide } from "@/components/onboarding/ConnectAgentGuide";

// Dashboard "connect your agent" card. Fix #1 renders the SDK quickstart; Fix #2
// makes it status-aware (online pill; hide the guide once the agent is connected).
export function ConnectAgentCard() {
  return (
    <Card className="p-5">
      <CardHeader title="Connect your agent" subtitle="Run it locally with the Onavion CLI — it plays live over a secure socket" />
      <div className="mt-4">
        <ConnectAgentGuide compact />
      </div>
    </Card>
  );
}
