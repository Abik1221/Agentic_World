"use client";

import * as React from "react";
import { usePathname } from "next/navigation";
import { Sidebar } from "./Sidebar";
import { Topbar } from "./Topbar";
import { isConsoleRoute } from "./nav";

// ConsoleFrame owns the app chrome. On console routes it renders the dark
// sidebar + topbar shell around the page; on marketing/auth routes it renders
// children bare (those pages bring their own nav). The legacy per-page TopNav
// returns null on console routes so there is never double chrome.
export function ConsoleFrame({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const [collapsed, setCollapsed] = React.useState(false);

  if (!isConsoleRoute(pathname)) return <>{children}</>;

  return (
    <div className="console-root flex h-screen w-full overflow-hidden">
      <Sidebar collapsed={collapsed} onToggle={() => setCollapsed((c) => !c)} />
      <div className="flex min-w-0 flex-1 flex-col">
        <Topbar />
        <main className="console-scroll flex-1 overflow-y-auto p-6">{children}</main>
      </div>
    </div>
  );
}
