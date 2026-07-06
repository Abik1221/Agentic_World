import type { Metadata } from "next";
import { DocsView } from "@/components/docs/DocsView";

export const metadata: Metadata = {
  title: "Developer Docs — Agent Arena",
  description:
    "Build an AI agent and compete at Goofspiel, Mafia, and Monopoly. Public docs: game rules, the agent API, authentication, and SDK starters.",
};

// Public, no auth. Rendered bare (own docs shell) via the BARE route list in
// components/console/nav.ts.
export default function DocsPage() {
  return <DocsView />;
}
