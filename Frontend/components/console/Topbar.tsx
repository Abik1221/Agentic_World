"use client";

import * as React from "react";
import { usePathname } from "next/navigation";
import { Bell, Search } from "lucide-react";
import { locate } from "./nav";

export function Topbar() {
  const pathname = usePathname();
  const { parent, child } = locate(pathname);

  return (
    <header className="sticky top-0 z-40 flex h-14 items-center gap-4 border-b border-line bg-canvas/95 px-6 backdrop-blur-sm">
      {/* Breadcrumb */}
      <div className="flex items-center gap-1.5 font-mono text-[11px] text-fg-muted">
        <span>Pyyol</span>
        {parent && (
          <>
            <span className="text-fg-muted/50">›</span>
            <span className={child ? "" : "text-fg"}>{parent.label}</span>
          </>
        )}
        {child && (
          <>
            <span className="text-fg-muted/50">›</span>
            <span className="text-fg">{child.label}</span>
          </>
        )}
      </div>

      <div className="flex-1" />

      {/* Search */}
      <button className="hidden items-center gap-2 rounded-md border border-line bg-panel-2/50 px-2.5 py-1.5 text-xs text-fg-muted transition-colors hover:text-fg sm:flex">
        <Search className="h-3.5 w-3.5" />
        <span>Search…</span>
        <kbd className="rounded border border-line bg-panel px-1 font-mono text-[10px]">⌘K</kbd>
      </button>

      {/* Notifications */}
      <button className="relative rounded-md p-1.5 text-fg-muted transition-colors hover:bg-elevated hover:text-fg">
        <Bell className="h-4 w-4" />
        <span className="absolute right-1.5 top-1.5 h-1.5 w-1.5 rounded-full bg-danger" />
      </button>

      {/* Live status */}
      <div className="hidden items-center gap-1.5 rounded-full border border-ok/20 px-2.5 py-1 md:flex">
        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-ok" />
        <span className="font-mono text-[10px] uppercase tracking-wider text-ok">Live</span>
      </div>

      {/* Avatar */}
      <div className="flex h-7 w-7 items-center justify-center rounded-full bg-brand/20 text-[11px] font-semibold text-brand">
        AA
      </div>
    </header>
  );
}
