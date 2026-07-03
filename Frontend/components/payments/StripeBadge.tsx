/** Stripe-only payment affordance — shown on checkout surfaces. */
export function StripeBadge({ className = "" }: { className?: string }) {
  return (
    <div
      className={`inline-flex items-center gap-2 rounded-md border border-border-strong bg-bg-deep/80 px-3 py-1.5 font-mono text-[10px] uppercase tracking-wider text-ink-dim ${className}`}
    >
      <span className="font-semibold text-[#635bff]">Stripe</span>
      <span className="text-ink-faint">·</span>
      <span>Card · Apple Pay · Google Pay · Link</span>
    </div>
  );
}
