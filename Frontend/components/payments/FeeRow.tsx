import { cx } from "@/components/ui";

export function FeeRow({
  label,
  value,
  tone,
}: {
  label: string;
  value: string;
  tone?: string;
}) {
  return (
    <div className="flex items-center justify-between font-mono text-sm">
      <span className="text-ink-dim">{label}</span>
      <span className={cx(tone ?? "text-ink-primary")}>{value}</span>
    </div>
  );
}
