"use client";

import { useCallback, useEffect, useState } from "react";
import NavLink from "./NavLink";
import type { ServiceHealth } from "@/lib/pyyol-lens-api";

const STORAGE_KEY = "pyyol-lens:sidebar-collapsed";

const nav = [
  { label: "Overview", href: "/overview" },
  { label: "Games", href: "/games" },
  { label: "Appeals", href: "/appeals" },
  { label: "Benchmarks", href: "/benchmarks" },
  { label: "Traces", href: "/traces" },
  { label: "Tool Calls", href: "/tool-calls" },
  { label: "Token Usage", href: "/token-usage" },
  { label: "Costs", href: "/costs" },
  { label: "Cost Analytics", href: "/cost-analytics" },
  { label: "Payments", href: "/payments" },
  { label: "Events", href: "/events" },
  { label: "Queue Health", href: "/queue" },
  // Alerts + Settings are hidden until the control-api backs them with real data
  // (their handlers are empty stubs today) — no dead surfaces in the nav.
];

export default function Shell({
  children,
  health,
  user,
}: {
  children: React.ReactNode;
  health?: ServiceHealth;
  user?: string;
}) {
  // Desktop: sidebar collapses to a rail. Mobile: sidebar is an off-canvas drawer
  // toggled by the hamburger; these two states are independent by design.
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [mobileOpen, setMobileOpen] = useState(false);

  useEffect(() => {
    try {
      const raw = window.localStorage.getItem(STORAGE_KEY);
      if (raw === "1") setSidebarCollapsed(true);
    } catch {
      /* ignore */
    }
  }, []);

  const setCollapsed = useCallback((next: boolean) => {
    setSidebarCollapsed(next);
    try {
      window.localStorage.setItem(STORAGE_KEY, next ? "1" : "0");
    } catch {
      /* ignore */
    }
  }, []);

  // Close the mobile drawer on Escape, and lock body scroll while it is open.
  useEffect(() => {
    if (!mobileOpen) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setMobileOpen(false);
    };
    document.addEventListener("keydown", onKey);
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = prev;
    };
  }, [mobileOpen]);

  return (
    <>
      <div
        className="shell"
        data-sidebar={sidebarCollapsed ? "collapsed" : "expanded"}
        data-mobile-open={mobileOpen ? "true" : "false"}
      >
        <aside
          className="sidebar"
          id="app-sidebar"
          aria-hidden={sidebarCollapsed ? true : undefined}
        >
          <div className="sidebar-toolbar">
            <button
              type="button"
              className="shell-sidebar-collapse-btn"
              onClick={() => setCollapsed(true)}
              aria-expanded={!sidebarCollapsed}
              aria-controls="app-sidebar"
              title="Collapse sidebar"
            >
              <span aria-hidden>«</span>
            </button>
          </div>
          <div className="brand">
            <div className="brand-mark">PE</div>
            <div>
              <p className="brand-title">Pyyol Eye</p>
              <p className="brand-subtitle">Arena telemetry</p>
            </div>
          </div>
          <nav className="nav">
            {nav.map((item) => (
              <NavLink
                key={item.href}
                href={item.href}
                label={item.label}
                onNavigate={() => setMobileOpen(false)}
              />
            ))}
          </nav>
          <div className="sidebar-footer">
            <p>Pyyol Eye</p>
            <p>ClickHouse + Go + Next.js</p>
          </div>
        </aside>
        <main className="content">
          <header className="topbar">
            <div className="topbar-left">
              <button
                type="button"
                className="shell-hamburger"
                onClick={() => setMobileOpen((v) => !v)}
                aria-expanded={mobileOpen}
                aria-controls="app-sidebar"
                aria-label={mobileOpen ? "Close navigation" : "Open navigation"}
              >
                <span aria-hidden>{mobileOpen ? "✕" : "☰"}</span>
              </button>
              <span className="workspace-pill">Pyyol Eye</span>
            </div>
            <div className="topbar-right">
              <StatusChip label="Query API" up={health?.query} />
              <StatusChip label="Ingest API" up={health?.ingest} />
              <StatusChip label="Control API" up={health?.control} />
              {user ? (
                <>
                  <span className="workspace-pill subtle" title="Signed in">
                    {user}
                  </span>
                  <button
                    type="button"
                    className="filter-chip"
                    onClick={async () => {
                      await fetch("/api/auth/logout", { method: "POST" });
                      window.location.reload();
                    }}
                    title="Sign out"
                  >
                    Sign out
                  </button>
                </>
              ) : null}
            </div>
          </header>
          <div className="content-body">{children}</div>
        </main>
      </div>
      {/* Mobile drawer backdrop — tap to close. */}
      <div
        className="shell-backdrop"
        data-visible={mobileOpen ? "true" : "false"}
        onClick={() => setMobileOpen(false)}
        aria-hidden
      />
      {sidebarCollapsed ? (
        <button
          type="button"
          className="shell-sidebar-reveal"
          onClick={() => setCollapsed(false)}
          aria-expanded={false}
          aria-controls="app-sidebar"
          title="Show sidebar"
        >
          <span aria-hidden>»</span>
        </button>
      ) : null}
    </>
  );
}

function StatusChip({ label, up }: { label: string; up?: boolean }) {
  // undefined = unknown (neutral), true = up (green), false = down (red).
  const cls = up === undefined ? "" : up ? " ok" : " down";
  const title = up === undefined ? `${label}: unknown` : up ? `${label}: healthy` : `${label}: unreachable`;
  return (
    <span className={`status-chip${cls}`} title={title}>
      {label}
    </span>
  );
}
