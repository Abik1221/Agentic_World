// Presentational helpers for the benchmark views. Pure, so both server pages
// share one implementation of formatting + threshold coloring.

/** Format a 0..1 rate as a percentage string. */
export function pct(v: number): string {
  return `${(Math.max(0, Math.min(1, v)) * 100).toFixed(1)}%`;
}

/** Compact token count: 1234 → "1.2k", 1_200_000 → "1.2M". */
export function ktoks(v: number): string {
  if (!v || v < 0) return "0";
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`;
  if (v >= 1000) return `${(v / 1000).toFixed(1)}k`;
  return `${Math.round(v)}`;
}

/** Format a latency in ms as a compact, human string. */
export function ms(v: number): string {
  if (!v || v < 0) return "0 ms";
  if (v >= 1000) return `${(v / 1000).toFixed(2)} s`;
  return `${Math.round(v)} ms`;
}

/** Status class for a "higher is better" rate (win/legal). */
export function goodRateClass(v: number): string {
  if (v >= 0.66) return "status-ok";
  if (v >= 0.33) return "status-warn";
  return "status-error";
}

/** Status class for a "lower is better" rate (fallback/timeout). */
export function badRateClass(v: number): string {
  if (v <= 0.05) return "status-ok";
  if (v <= 0.2) return "status-warn";
  return "status-error";
}

export const GAMES = ["goofspiel", "mafia", "monopoly"] as const;
export const DAY_WINDOWS = [7, 30, 90] as const;

/** Build an href preserving the other query params (for filter chips). */
export function withParam(
  base: string,
  current: Record<string, string | undefined>,
  key: string,
  value: string | undefined,
): string {
  const next: Record<string, string> = {};
  for (const [k, v] of Object.entries(current)) {
    if (v) next[k] = v;
  }
  if (value === undefined || value === "") delete next[key];
  else next[key] = value;
  const qs = new URLSearchParams(next).toString();
  return qs ? `${base}?${qs}` : base;
}
