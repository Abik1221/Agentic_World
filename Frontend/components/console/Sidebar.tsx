"use client";

import * as React from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { ChevronDown, ChevronLeft, ChevronRight, Zap } from "lucide-react";
import { cn } from "@/lib/cn";
import { NAV, locate } from "./nav";

export function Sidebar({
  collapsed,
  onToggle,
}: {
  collapsed: boolean;
  onToggle: () => void;
}) {
  const pathname = usePathname();
  const { parent } = locate(pathname);
  const activeParent = parent?.id;

  const [expanded, setExpanded] = React.useState<string[]>(() =>
    activeParent ? [activeParent] : ["games"],
  );

  // Keep the active group open as the route changes.
  React.useEffect(() => {
    if (activeParent) setExpanded((e) => (e.includes(activeParent) ? e : [...e, activeParent]));
  }, [activeParent]);

  const toggleGroup = (id: string) =>
    setExpanded((e) => (e.includes(id) ? e.filter((x) => x !== id) : [...e, id]));

  return (
    <aside
      className={cn(
        "flex h-full shrink-0 flex-col border-r border-sidebar-line bg-sidebar-bg transition-all duration-200",
        collapsed ? "w-14" : "w-52",
      )}
    >
      {/* Logo */}
      <div
        className={cn(
          "flex h-14 shrink-0 items-center gap-2.5 border-b border-sidebar-line px-3.5",
          collapsed && "justify-center px-0",
        )}
      >
        <div className="flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-brand">
          <Zap className="h-4 w-4 text-white" />
        </div>
        {!collapsed && (
          <div className="leading-tight">
            <p className="text-xs font-semibold text-fg">Onavion</p>
            <p className="font-mono text-[10px] leading-tight text-fg-muted">Agent Arena</p>
          </div>
        )}
      </div>

      {/* Nav */}
      <nav className="console-scroll flex-1 space-y-0.5 overflow-y-auto px-2 py-3">
        {NAV.map((item) => {
          const isActive = activeParent === item.id;
          const isOpen = expanded.includes(item.id);
          const Icon = item.icon;
          const firstChild = item.children?.[0];

          const content = (
            <>
              <Icon className="h-4 w-4 shrink-0" />
              {!collapsed && (
                <>
                  <span className="flex-1 truncate text-xs">{item.label}</span>
                  {item.badge && (
                    <span className="rounded bg-danger/15 px-1.5 py-0.5 font-mono text-[10px] leading-none text-danger">
                      {item.badge}
                    </span>
                  )}
                  {item.children && (
                    <ChevronDown
                      className={cn("h-3 w-3 text-fg-muted transition-transform duration-150", isOpen && "rotate-180")}
                    />
                  )}
                </>
              )}
            </>
          );

          const cls = cn(
            "flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-left transition-colors",
            collapsed && "justify-center px-0",
            isActive
              ? "bg-elevated text-fg"
              : "text-fg-muted hover:bg-elevated/50 hover:text-fg",
          );

          return (
            <div key={item.id}>
              {item.children ? (
                collapsed ? (
                  <Link href={firstChild!.href} title={item.label} className={cls}>
                    {content}
                  </Link>
                ) : (
                  <button title={item.label} onClick={() => toggleGroup(item.id)} className={cls}>
                    {content}
                  </button>
                )
              ) : (
                <Link href={item.href!} title={collapsed ? item.label : undefined} className={cls}>
                  {content}
                </Link>
              )}

              {!collapsed && item.children && isOpen && (
                <div className="ml-6 mt-0.5 space-y-0.5 border-l border-line pl-2">
                  {item.children.map((child) => {
                    const childActive = pathname === child.href || pathname.startsWith(child.href + "/");
                    return (
                      <Link
                        key={child.href}
                        href={child.href}
                        className={cn(
                          "block rounded px-2 py-1.5 text-xs transition-colors",
                          childActive
                            ? "bg-elevated/40 font-medium text-fg"
                            : "text-fg-muted hover:bg-elevated/50 hover:text-fg",
                        )}
                      >
                        {child.label}
                      </Link>
                    );
                  })}
                </div>
              )}
            </div>
          );
        })}
      </nav>

      {/* Collapse toggle */}
      <div className="border-t border-sidebar-line p-2">
        <button
          onClick={onToggle}
          className="flex w-full items-center justify-center rounded-md p-2 text-fg-muted transition-colors hover:bg-elevated/50 hover:text-fg"
        >
          {collapsed ? <ChevronRight className="h-4 w-4" /> : <ChevronLeft className="h-4 w-4" />}
        </button>
      </div>
    </aside>
  );
}
