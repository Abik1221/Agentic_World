import {
  LayoutDashboard,
  Gamepad2,
  FlaskConical,
  Radio,
  Swords,
  Bot,
  CircleDollarSign,
  User,
  Shield,
  BookOpen,
  type LucideIcon,
} from "lucide-react";

export type NavChild = { label: string; href: string };
export type NavItem = {
  id: string;
  label: string;
  icon: LucideIcon;
  href?: string;
  badge?: string;
  children?: NavChild[];
};

// Sidebar information architecture. Parents with children act as categories
// (e.g. Games → each game); clicking a parent opens the group and lands on its
// first child. Every href maps to a real route.
export const NAV: NavItem[] = [
  { id: "overview", label: "Overview", icon: LayoutDashboard, href: "/dashboard" },
  {
    id: "games",
    label: "Games",
    icon: Gamepad2,
    children: [
      { label: "Mafia", href: "/arena/mafia" },
      { label: "Monopoly", href: "/monopoly" },
      { label: "Goofspiel", href: "/goofspiel" },
    ],
  },
  {
    id: "sandbox",
    label: "Sandbox",
    icon: FlaskConical,
    children: [
      { label: "Goofspiel Sandbox", href: "/sandbox/goofspiel" },
      { label: "Mafia Sandbox", href: "/sandbox/mafia" },
      { label: "Monopoly Sandbox", href: "/sandbox/monopoly" },
    ],
  },
  {
    id: "watch",
    label: "Watch",
    icon: Radio,
    children: [
      { label: "Live Matches", href: "/spectate" },
      { label: "Lobby", href: "/lobby" },
      { label: "Clips", href: "/clips" },
    ],
  },
  {
    id: "compete",
    label: "Compete",
    icon: Swords,
    children: [
      { label: "Quick Play", href: "/play" },
      { label: "Rankings", href: "/rankings" },
      { label: "Tournaments", href: "/tournaments" },
    ],
  },
  {
    id: "agents",
    label: "My Agents",
    icon: Bot,
    children: [
      { label: "API Keys", href: "/keys" },
      { label: "Endpoint", href: "/manifest" },
      { label: "Strategy", href: "/strategy" },
      { label: "Guardrails", href: "/guardrails" },
    ],
  },
  {
    id: "wallet",
    label: "Wallet",
    icon: CircleDollarSign,
    children: [
      { label: "Balance", href: "/wallet" },
      { label: "Withdrawals", href: "/withdrawals" },
      { label: "Arena Pass", href: "/subscription" },
    ],
  },
  { id: "profile", label: "Profile", icon: User, href: "/profile" },
  { id: "docs", label: "Docs", icon: BookOpen, href: "/docs" },
  {
    id: "admin",
    label: "Admin",
    icon: Shield,
    children: [{ label: "Withdrawals", href: "/admin/withdrawals" }],
  },
];

// Routes that render WITHOUT the console chrome (marketing + auth + hosted
// return pages). Everything else is a console route (sidebar + topbar).
const BARE_EXACT = new Set(["/", "/welcome", "/login", "/register", "/verify", "/provision", "/docs"]);
const BARE_PREFIX = ["/auth", "/billing", "/payouts", "/subscription/success", "/subscription/cancel"];

export function isConsoleRoute(pathname: string): boolean {
  if (BARE_EXACT.has(pathname)) return false;
  if (BARE_PREFIX.some((p) => pathname === p || pathname.startsWith(p + "/"))) return false;
  return true;
}

// Resolve the (parent, child) labels for a path, for breadcrumbs + active state.
export function locate(pathname: string): { parent?: NavItem; child?: NavChild } {
  for (const item of NAV) {
    if (item.href && (pathname === item.href || pathname.startsWith(item.href + "/"))) {
      return { parent: item };
    }
    if (item.children) {
      const child = item.children.find(
        (c) => pathname === c.href || pathname.startsWith(c.href + "/"),
      );
      if (child) return { parent: item, child };
    }
  }
  return {};
}
