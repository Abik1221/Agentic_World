"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import { Brand, Pill, cx } from "./ui";
import { Home, Trophy, Gear, Wallet, Eye, Cpu } from "./icons";
import { getSession, clearSession } from "@/lib/session";
import { showAdminNav } from "@/lib/admin";
import { getProfile, completionPercent, onProfileChange } from "@/lib/profile";
import { ThemeToggle } from "./ThemeToggle";

// Navbar holds only game/content destinations. Payment & account surfaces
// (Wallet, Pass, Stakes, Withdrawals) live under the user's Profile.
// The three games are grouped under a single "Games" circle (see GamesMenu).
const games = [
  { href: "/goofspiel", label: "Goofspiel", color: "#3b82f6" }, // blue
  { href: "/mafia", label: "Mafia", color: "#8b5cf6" }, // violet
  { href: "/monopoly", label: "Monopoly", color: "#10b981" }, // emerald
];

const navLinks = [
  { href: "/spectate", label: "Watch" },
  { href: "/lobby", label: "Lobby" },
  { href: "/play", label: "Play" },
  { href: "/rankings", label: "Rankings" },
  { href: "/clips", label: "Clips" },
];

function NavItem({ l, pathname }: { l: { href: string; label: string }; pathname: string }) {
  const active = pathname === l.href;
  return (
    <Link
      href={l.href}
      className={cx(
        "font-sans text-sm transition",
        active
          ? "text-ink-primary underline decoration-primary decoration-2 underline-offset-8"
          : "text-ink-dim hover:text-ink-primary",
      )}
    >
      {l.label}
    </Link>
  );
}

// GamesMenu collapses the three games into one circular control. The circle
// itself "defines games" — it holds a dot per game in that game's accent color —
// and opens a dropdown listing them.
function GamesMenu() {
  const pathname = usePathname();
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const activeGame = games.find((g) => pathname === g.href);

  useEffect(() => setOpen(false), [pathname]);
  useEffect(() => {
    function onDoc(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, []);

  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="menu"
        aria-expanded={open}
        className={cx(
          "flex items-center gap-2 font-sans text-sm transition",
          activeGame || open ? "text-ink-primary" : "text-ink-dim hover:text-ink-primary",
        )}
      >
        {/* The circle that defines "Games": one dot per game, in its accent color. */}
        <span
          className={cx(
            "grid h-6 w-6 place-items-center rounded-full border bg-surface-bright transition",
            activeGame || open ? "border-primary shadow-glow-teal" : "border-border-strong",
          )}
        >
          <span className="flex items-center gap-[2px]">
            {games.map((g) => (
              <span key={g.href} className="h-[3px] w-[3px] rounded-full" style={{ background: g.color }} />
            ))}
          </span>
        </span>
        <span>{activeGame ? activeGame.label : "Games"}</span>
        <svg width="10" height="10" viewBox="0 0 10 10" className={cx("transition-transform", open && "rotate-180")}>
          <path d="M2 3.5L5 6.5L8 3.5" stroke="currentColor" strokeWidth="1.4" fill="none" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      </button>

      {open && (
        <div
          role="menu"
          className="modal-in absolute left-0 top-full z-40 mt-2 w-44 overflow-hidden rounded-lg border border-border-strong bg-surface-bright shadow-[0_16px_40px_-12px_rgba(24,27,32,0.25)]"
        >
          <div className="border-b border-border-soft px-3 py-2">
            <span className="label-caps text-ink-faint">Games</span>
          </div>
          {games.map((g) => {
            const active = pathname === g.href;
            return (
              <Link
                key={g.href}
                href={g.href}
                role="menuitem"
                className={cx(
                  "flex items-center gap-2.5 px-3 py-2.5 font-sans text-sm transition",
                  active
                    ? "bg-primary/10 text-ink-primary"
                    : "text-ink-dim hover:bg-surface hover:text-ink-primary",
                )}
              >
                <span className="h-2 w-2 rounded-full" style={{ background: g.color }} />
                {g.label}
              </Link>
            );
          })}
        </div>
      )}
    </div>
  );
}

// Desktop top navigation bar
export function TopNav({ pool = "1.2M pool" }: { pool?: string }) {
  const pathname = usePathname();
  const [authed, setAuthed] = useState(false);
  const [adminNav, setAdminNav] = useState(false);
  const [avatar, setAvatar] = useState("");
  const [pct, setPct] = useState(100);

  useEffect(() => {
    const refresh = () => {
      const s = getSession();
      const hasSession = Boolean(s.dashboardToken || s.apiKey);
      setAuthed(hasSession);
      setAdminNav(showAdminNav());
      setAvatar(getProfile().avatar);
      setPct(completionPercent({ hasSession }));
    };
    refresh();
    return onProfileChange(refresh);
  }, [pathname]);

  function signOut() {
    clearSession();
    setAuthed(false);
    window.location.href = "/";
  }

  const incomplete = authed && pct < 100;

  return (
    <header className="sticky top-0 z-30 border-b border-border-soft bg-bg-deep/80 backdrop-blur-md">
      {incomplete && (
        <Link
          href="/profile"
          className="flex items-center justify-center gap-2 border-b border-secondary/30 bg-secondary/10 px-4 py-1.5 font-mono text-[11px] text-secondary transition hover:bg-secondary/15"
        >
          <span className="live-dot h-1.5 w-1.5 rounded-full bg-secondary" />
          Your profile is {pct}% complete — finish setup to unlock paid matches & withdrawals →
        </Link>
      )}
      <div className="mx-auto flex h-16 max-w-container items-center justify-between px-6">
        <div className="flex items-center gap-10">
          <Brand />
          <nav className="hidden items-center gap-6 md:flex">
            <NavItem l={navLinks[0]} pathname={pathname} />
            <GamesMenu />
            {navLinks.slice(1).map((l) => (
              <NavItem key={l.href} l={l} pathname={pathname} />
            ))}
          </nav>
        </div>
        <div className="flex items-center gap-3">
          <ThemeToggle />
          <Link href="/dashboard" className="btn-ghost">
            Console
          </Link>
          {adminNav && (
            <Link href="/admin/withdrawals" className="btn-ghost hidden sm:inline-flex">
              Admin
            </Link>
          )}
          {authed ? (
            <>
              <Link
                href="/profile"
                title="Your agent profile"
                className={cx(
                  "relative grid h-9 w-9 place-items-center overflow-hidden rounded-full border transition",
                  incomplete ? "border-secondary/60" : "border-primary-container/50",
                )}
              >
                {avatar ? (
                  // eslint-disable-next-line @next/next/no-img-element
                  <img src={avatar} alt="Your agent" className="h-full w-full object-cover" />
                ) : (
                  <Cpu width={16} height={16} />
                )}
                {incomplete && (
                  <span className="absolute -right-0.5 -top-0.5 h-2.5 w-2.5 rounded-full border-2 border-bg-deep bg-secondary" />
                )}
              </Link>
              <button onClick={signOut} className="btn-neutral hidden sm:inline-flex">
                Sign out
              </button>
            </>
          ) : (
            <Link href="/login" className="btn-neutral hidden sm:inline-flex">
              Sign in
            </Link>
          )}
        </div>
      </div>
    </header>
  );
}

// Mobile bottom tab bar (used on the phone-framed screens)
const tabs = [
  { href: "/dashboard", label: "Dashboard", Icon: Home },
  { href: "/rankings", label: "Ranking", Icon: Trophy },
  { href: "/wallet", label: "Wallet", Icon: Wallet },
  { href: "/strategy", label: "Settings", Icon: Gear },
];

export function BottomTabs() {
  const pathname = usePathname();
  return (
    <nav className="sticky bottom-0 z-30 border-t border-border-soft bg-bg-deep/95 backdrop-blur-md">
      <div className="mx-auto grid max-w-md grid-cols-4">
        {tabs.map(({ href, label, Icon }) => {
          const active = pathname === href;
          return (
            <Link
              key={href}
              href={href}
              className={cx(
                "flex flex-col items-center gap-1 py-3 transition",
                active ? "text-primary" : "text-ink-faint hover:text-ink-dim",
              )}
            >
              <Icon width={20} height={20} />
              <span className="label-caps text-[9px]">{label}</span>
            </Link>
          );
        })}
      </div>
    </nav>
  );
}

// Onboarding step indicator (e.g. "PROVISIONING: 4/4")
export function StepBar({ step, total, label }: { step: number; total: number; label: string }) {
  return (
    <div>
      <div className="mb-2 flex items-center justify-between">
        <span className="label-caps text-primary">{label}</span>
        <span className="font-mono text-[11px] text-ink-dim">
          {step}/{total}
        </span>
      </div>
      <div className="flex gap-1.5">
        {Array.from({ length: total }, (_, i) => (
          <div
            key={i}
            className={cx(
              "h-1 flex-1 rounded-full",
              i < step ? "bg-primary-container shadow-glow-teal" : "bg-border-strong",
            )}
          />
        ))}
      </div>
    </div>
  );
}

export { Cpu, Eye };
