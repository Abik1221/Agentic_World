/** Saturated mid/light fills for dark-background span/event chips and bars. */

export const SPAN_COLORS: Record<string, string> = {
  llm: "#a78bfa",
  request: "#38bdf8",
  function: "#4ade80",
  tool: "#fbbf24",
  tool_call: "#fbbf24",
  retrieval: "#34d399",
  embedding: "#22d3ee",
  operation: "#94a3b8",
  agent_call: "#818cf8",
  match: "#f472b6",
  decision: "#2dd4bf",
  model_call: "#c084fc",
  trace_completion: "#67e8f9",
  chat: "#fb923c",
  lifecycle: "#a3e635",
  log: "#e2e8f0",
  agent_notify: "#e879f9",
  benchmark: "#facc15",
  domain_event: "#60a5fa",
  agent_turn: "#f9a8d4",
  trace: "#94a3b8",
};

const TYPE_ALIASES: Record<string, string> = {
  agent_decision: "decision",
  agent_said: "chat",
  agent_say: "chat",
  agent_say_rejected: "chat",
  agent_message: "chat",
  llm_call: "llm",
  function_call: "function",
  tool_use: "tool_call",
  trace_completed: "trace_completion",
  trace_failed: "trace_completion",
  notify: "agent_notify",
};

const SUFFIX = /_(started|completed|failed|retried|rejected)$/;

export function normalizeSpanType(type: string | undefined | null): string {
  const raw = (type || "").toLowerCase().trim();
  if (!raw) return "operation";
  const aliased = TYPE_ALIASES[raw];
  if (aliased) return aliased;
  const stripped = raw.replace(SUFFIX, "");
  return TYPE_ALIASES[stripped] ?? stripped;
}

export function colorForSpanType(type: string | undefined | null, status?: string): string {
  if (status === "error") return "#f87171";
  const key = normalizeSpanType(type);
  return SPAN_COLORS[key] ?? "#94a3b8";
}

export function colorForEventType(type: string | undefined | null, status?: string): string {
  return colorForSpanType(type, status);
}

function parseHex(hex: string): { r: number; g: number; b: number } {
  const h = hex.replace("#", "");
  const full = h.length === 3 ? h.split("").map((c) => c + c).join("") : h;
  const n = Number.parseInt(full.slice(0, 6), 16);
  if (!Number.isFinite(n)) return { r: 148, g: 163, b: 184 };
  return { r: (n >> 16) & 255, g: (n >> 8) & 255, b: n & 255 };
}

export function withAlpha(hex: string, alpha: number): string {
  const { r, g, b } = parseHex(hex);
  return `rgba(${r}, ${g}, ${b}, ${alpha})`;
}

export function relativeLuminance(hex: string): number {
  const { r, g, b } = parseHex(hex);
  const lin = (c: number) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

/** Dark text on light fills, light text on mid/dark fills. */
export function labelColorForFill(hex: string): string {
  return relativeLuminance(hex) > 0.42 ? "#0f172a" : "#f8fafc";
}

export function spanTypeChipStyle(type: string | undefined | null, status?: string): {
  background: string;
  borderColor: string;
  color: string;
} {
  const fill = colorForSpanType(type, status);
  return {
    background: withAlpha(fill, 0.18),
    borderColor: withAlpha(fill, 0.55),
    color: fill,
  };
}

export function uniqueSpanTypes(types: Array<string | undefined | null>): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const t of types) {
    const key = normalizeSpanType(t);
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(key);
  }
  return out.sort();
}
