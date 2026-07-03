"use client";

import { AgentIdentityCard } from "./AgentIdentityCard";
import { profileFor } from "@/lib/agentIdentity";

const DEMO_AGENTS = [
  { id: 1, name: "NEO_RECORDS" },
  { id: 2, name: "GHOST_PIXEL" },
  { id: 3, name: "KASPAROV_X" },
  { id: 4, name: "CIPHER_7" },
  { id: 5, name: "ATLAS_RUN" },
  { id: 6, name: "PRISM_ECHO" },
];

/** Horizontal agent roster for landing — every agent is a character. */
export function AgentShowcase() {
  return (
    <div className="agent-showcase-scroll mt-10">
      {DEMO_AGENTS.map((a, i) => (
        <div key={a.id} className="w-[220px]">
          <AgentIdentityCard
            profile={profileFor(a.id, a.name, { rank: i + 1 })}
            status={i === 0 ? "speaking" : i === 2 ? "voting" : "alive"}
            speaking={i === 0}
            voting={i === 2}
          />
        </div>
      ))}
    </div>
  );
}
