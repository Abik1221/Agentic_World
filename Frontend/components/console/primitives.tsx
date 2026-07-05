import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { Slot } from "@radix-ui/react-slot";
import { TrendingUp, TrendingDown, type LucideIcon } from "lucide-react";
import { cn } from "@/lib/cn";

/* ─────────────────────────────────────────── Card ─────────────────────────── */
export function Card({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("rounded-lg border border-line bg-panel", className)} {...props} />;
}

export function CardHeader({
  title,
  subtitle,
  action,
  className,
}: {
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  action?: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex items-start justify-between gap-3", className)}>
      <div>
        <h3 className="text-sm font-semibold text-fg">{title}</h3>
        {subtitle && <p className="mt-0.5 font-mono text-[11px] text-fg-muted">{subtitle}</p>}
      </div>
      {action}
    </div>
  );
}

/* ───────────────────────────────────────── PageHeader ─────────────────────── */
export function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  actions?: React.ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-4">
      <div>
        <h1 className="text-xl font-semibold tracking-tight text-fg">{title}</h1>
        {subtitle && <p className="mt-1 font-mono text-xs text-fg-muted">{subtitle}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}

/* ───────────────────────────────────────── KPI card ───────────────────────── */
export function KpiCard({
  label,
  value,
  sub,
  trend,
  trendUp,
  icon: Icon,
}: {
  label: string;
  value: React.ReactNode;
  sub?: React.ReactNode;
  trend?: string;
  trendUp?: boolean;
  icon: LucideIcon;
}) {
  return (
    <div className="group flex flex-col gap-3 rounded-lg border border-line bg-panel p-5 transition-colors duration-200 hover:border-brand/30">
      <div className="flex items-start justify-between">
        <p className="text-[10px] font-mono uppercase leading-none tracking-widest text-fg-muted">{label}</p>
        <div className="rounded-md bg-brand/10 p-1.5 transition-colors group-hover:bg-brand/20">
          <Icon className="h-3.5 w-3.5 text-brand" />
        </div>
      </div>
      <div>
        <p className="text-2xl font-semibold leading-none tracking-tight text-fg">{value}</p>
        {sub && <p className="mt-1.5 font-mono text-[11px] leading-snug text-fg-muted">{sub}</p>}
      </div>
      {trend && (
        <div className={cn("flex items-center gap-1 font-mono text-[11px]", trendUp ? "text-ok" : "text-danger")}>
          {trendUp ? <TrendingUp className="h-3 w-3" /> : <TrendingDown className="h-3 w-3" />}
          {trend}
        </div>
      )}
    </div>
  );
}

export function StatCard({
  label,
  value,
  icon: Icon,
  color = "text-brand",
}: {
  label: string;
  value: React.ReactNode;
  icon: LucideIcon;
  color?: string;
}) {
  return (
    <div className="flex items-center justify-between rounded-lg border border-line bg-panel px-4 py-3">
      <div>
        <p className="text-[10px] font-mono uppercase tracking-widest text-fg-muted">{label}</p>
        <p className="mt-1 text-xl font-semibold text-fg">{value}</p>
      </div>
      <Icon className={cn("h-5 w-5", color)} />
    </div>
  );
}

/* ─────────────────────────────────────── StatusBadge ──────────────────────── */
const STATUS_MAP: Record<string, string> = {
  live: "bg-ok/10 text-ok border-ok/20",
  active: "bg-ok/10 text-ok border-ok/20",
  healthy: "bg-ok/10 text-ok border-ok/20",
  online: "bg-ok/10 text-ok border-ok/20",
  ranked: "bg-brand/10 text-brand border-brand/25",
  running: "bg-brand/10 text-brand border-brand/25",
  pending: "bg-warn/10 text-warn border-warn/20",
  warning: "bg-warn/10 text-warn border-warn/20",
  suspended: "bg-danger/10 text-danger border-danger/20",
  failed: "bg-danger/10 text-danger border-danger/20",
  critical: "bg-danger/10 text-danger border-danger/20",
  draft: "bg-panel-2 text-fg-muted border-line",
  inactive: "bg-panel-2 text-fg-muted border-line",
};

export function StatusBadge({ status, className }: { status: string; className?: string }) {
  const tone = STATUS_MAP[status.toLowerCase()] ?? STATUS_MAP.draft;
  return (
    <span
      className={cn(
        "inline-flex items-center whitespace-nowrap rounded border px-2 py-0.5 font-mono text-xs capitalize",
        tone,
        className,
      )}
    >
      {status}
    </span>
  );
}

/* ─────────────────────────────────────── ProgressBar ──────────────────────── */
export function ProgressBar({ value, color, className }: { value: number; color?: string; className?: string }) {
  return (
    <div className={cn("h-1.5 w-full overflow-hidden rounded-full bg-panel-2", className)}>
      <div
        className="h-full rounded-full transition-all"
        style={{ width: `${Math.max(0, Math.min(100, value))}%`, background: color ?? "rgb(99 102 241)" }}
      />
    </div>
  );
}

/* ────────────────────────────────────────── Button ────────────────────────── */
const buttonVariants = cva(
  "inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium outline-none transition-all focus-visible:ring-2 focus-visible:ring-brand/50 disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "bg-brand text-brand-fg hover:bg-brand/90",
        outline: "border border-line bg-transparent text-fg hover:bg-elevated hover:border-brand/30",
        secondary: "bg-panel-2 text-fg hover:bg-elevated",
        ghost: "text-fg-muted hover:bg-elevated hover:text-fg",
        destructive: "bg-danger text-white hover:bg-danger/90",
        link: "text-brand underline-offset-4 hover:underline",
      },
      size: {
        default: "h-9 px-4 py-2",
        sm: "h-8 gap-1.5 rounded-md px-3",
        lg: "h-10 rounded-md px-6",
        icon: "size-9 rounded-md",
      },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);

export interface ButtonProps
  extends React.ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  asChild?: boolean;
}

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, asChild = false, ...props }, ref) => {
    const Comp = asChild ? Slot : "button";
    return <Comp ref={ref} className={cn(buttonVariants({ variant, size }), className)} {...props} />;
  },
);
Button.displayName = "Button";

/* ─────────────────────────────────────────── Badge ────────────────────────── */
export function Badge({
  children,
  tone = "brand",
  className,
}: {
  children: React.ReactNode;
  tone?: "brand" | "ok" | "warn" | "danger" | "muted";
  className?: string;
}) {
  const tones: Record<string, string> = {
    brand: "bg-brand/10 text-brand border-brand/25",
    ok: "bg-ok/10 text-ok border-ok/20",
    warn: "bg-warn/10 text-warn border-warn/20",
    danger: "bg-danger/10 text-danger border-danger/20",
    muted: "bg-panel-2 text-fg-muted border-line",
  };
  return (
    <span
      className={cn(
        "inline-flex w-fit items-center gap-1 rounded-md border px-2 py-0.5 font-mono text-[10px] uppercase tracking-wider",
        tones[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}

/* ─────────────────────────────────────── EmptyState ───────────────────────── */
export function EmptyState({ icon: Icon, title, hint }: { icon: LucideIcon; title: string; hint?: string }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-line bg-panel/40 py-12 text-center">
      <Icon className="h-6 w-6 text-fg-muted" />
      <p className="text-sm font-medium text-fg">{title}</p>
      {hint && <p className="max-w-xs font-mono text-[11px] text-fg-muted">{hint}</p>}
    </div>
  );
}

/* ─────────────────────────────────────────── SubNav ──────────────────────── */
// In-page secondary navigation (admin-style section tabs). Presentational —
// the page owns the active state.
export function SubNav<T extends string>({
  items,
  active,
  onSelect,
  className,
}: {
  items: { key: T; label: string; count?: number }[];
  active: T;
  onSelect: (k: T) => void;
  className?: string;
}) {
  return (
    <div className={cn("flex items-center gap-1 overflow-x-auto border-b border-line", className)}>
      {items.map((it) => (
        <button
          key={it.key}
          onClick={() => onSelect(it.key)}
          className={cn(
            "relative whitespace-nowrap px-3 py-2 text-xs font-medium transition-colors",
            active === it.key ? "text-fg" : "text-fg-muted hover:text-fg",
          )}
        >
          {it.label}
          {typeof it.count === "number" && (
            <span className="ml-1.5 rounded bg-panel-2 px-1 font-mono text-[10px] text-fg-muted">{it.count}</span>
          )}
          {active === it.key && <span className="absolute inset-x-2 -bottom-px h-0.5 rounded-full bg-brand" />}
        </button>
      ))}
    </div>
  );
}

/* ────────────────────────────────────────── DataTable ────────────────────── */
export type Column<Row> = {
  key: string;
  header: string;
  align?: "left" | "right";
  render: (row: Row) => React.ReactNode;
};

export function DataTable<Row>({
  columns,
  rows,
  empty = "No records",
}: {
  columns: Column<Row>[];
  rows: Row[];
  empty?: React.ReactNode;
}) {
  return (
    <div className="overflow-x-auto rounded-lg border border-line">
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-line bg-panel-2/40">
            {columns.map((c) => (
              <th
                key={c.key}
                className={cn(
                  "px-4 py-2.5 font-mono text-[10px] font-medium uppercase tracking-widest text-fg-muted",
                  c.align === "right" ? "text-right" : "text-left",
                )}
              >
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr>
              <td colSpan={columns.length} className="px-4 py-10 text-center text-xs text-fg-muted">
                {empty}
              </td>
            </tr>
          ) : (
            rows.map((r, i) => (
              <tr key={i} className="border-b border-line/60 transition-colors last:border-0 hover:bg-elevated/30">
                {columns.map((c) => (
                  <td key={c.key} className={cn("px-4 py-2.5 text-fg", c.align === "right" ? "text-right" : "text-left")}>
                    {c.render(r)}
                  </td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}

/* ─────────────────────────────── recharts shared styling ──────────────────── */
export const chartTooltipStyle = {
  background: "#111118",
  border: "1px solid rgba(255,255,255,0.07)",
  borderRadius: "6px",
  fontSize: "11px",
  fontFamily: "JetBrains Mono, monospace",
  color: "#e2e2ea",
} as const;

export const axisTick = { fontSize: 9, fill: "#8d8da1", fontFamily: "JetBrains Mono, monospace" } as const;
export const chartGrid = "rgba(255,255,255,0.04)";

// Canonical categorical palette — identical to the admin's --chart-1..5.
export const CHART = {
  brand: "#6366f1",
  ok: "#22c55e",
  warn: "#f59e0b",
  danger: "#ef4444",
  info: "#818cf8",
  muted: "#2a2a3c",
} as const;
