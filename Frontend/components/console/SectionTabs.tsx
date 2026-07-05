"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/cn";
import { NAV } from "./nav";

// SectionTabs renders the current section's child routes as an in-page sub-nav
// (the section "sub-side bar"), matching the admin's sub-route tabs. It derives
// the tabs from the same NAV the sidebar uses, so the two never drift.
export function SectionTabs() {
  const pathname = usePathname();
  const section = NAV.find((item) =>
    item.children?.some((c) => pathname === c.href || pathname.startsWith(c.href + "/")),
  );
  if (!section?.children) return null;

  return (
    <div className="flex items-center gap-1 overflow-x-auto border-b border-line">
      {section.children.map((c) => {
        const active = pathname === c.href || pathname.startsWith(c.href + "/");
        return (
          <Link
            key={c.href}
            href={c.href}
            className={cn(
              "relative whitespace-nowrap px-3 py-2 text-xs font-medium transition-colors",
              active ? "text-fg" : "text-fg-muted hover:text-fg",
            )}
          >
            {c.label}
            {active && <span className="absolute inset-x-2 -bottom-px h-0.5 rounded-full bg-brand" />}
          </Link>
        );
      })}
    </div>
  );
}
