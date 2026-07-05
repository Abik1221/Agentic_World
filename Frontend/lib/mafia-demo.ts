// Scripted demo data for the Mafia live-match viewer. This drives a self-running
// "broadcast" so the experience is fully alive without a backend — the real
// socket feed can later replace the ticker with the same shapes.

export type Intent =
  | "thinking"
  | "accusing"
  | "defending"
  | "negotiating"
  | "reasoning"
  | "voting"
  | "bluffing"
  | "analyzing";

export const INTENT: Record<Intent, { icon: string; label: string; color: string }> = {
  thinking: { icon: "🧠", label: "Thinking", color: "#818cf8" },
  accusing: { icon: "👉", label: "Accusing", color: "#ef4444" },
  defending: { icon: "🛡", label: "Defending", color: "#6366f1" },
  negotiating: { icon: "🤝", label: "Negotiating", color: "#14b8a6" },
  reasoning: { icon: "⚖", label: "Reasoning", color: "#22c55e" },
  voting: { icon: "🗳", label: "Voting", color: "#f59e0b" },
  bluffing: { icon: "🎭", label: "Bluffing", color: "#ec4899" },
  analyzing: { icon: "📊", label: "Analyzing", color: "#8b5cf6" },
};

export type AgentStatus =
  | "idle"
  | "thinking"
  | "llm"
  | "analyzing"
  | "speaking"
  | "voting"
  | "waiting"
  | "dead";

// Hidden role — concealed while the match is live, unsealed on the end-game reveal.
export type Role = "mafia" | "detective" | "doctor" | "citizen";

export const ROLE_META: Record<Role, { label: string; color: string; side: "town" | "mafia"; glyph: string; desc: string }> = {
  mafia: { label: "Mafia", color: "#ef4444", side: "mafia", glyph: "🔪", desc: "Secretly eliminated civilians each night." },
  detective: { label: "Detective", color: "#38bdf8", side: "town", glyph: "🔍", desc: "Investigated one player each night." },
  doctor: { label: "Doctor", color: "#34d399", side: "town", glyph: "✚", desc: "Protected one player each night." },
  citizen: { label: "Citizen", color: "#94a3b8", side: "town", glyph: "👤", desc: "Worked with the town to identify the Mafia." },
};

export type Agent = {
  id: string;
  name: string;
  dev: string;
  color: string;
  model: string;
  sdk: string;
  manifest: string;
  winRate: number;
  games: number;
  responseMs: number;
  role: Role; // revealed only at match end
};

export const AGENTS: Agent[] = [
  { id: "A", name: "Cascade", dev: "nova-labs", color: "#6366f1", model: "claude-opus-4-8", sdk: "1.4.0", manifest: "2.0", winRate: 62, games: 418, responseMs: 740, role: "citizen" },
  { id: "B", name: "Vesper", dev: "greypack", color: "#8b5cf6", model: "gpt-5", sdk: "1.4.0", manifest: "2.0", winRate: 55, games: 302, responseMs: 910, role: "mafia" },
  { id: "C", name: "Marlow", dev: "tinker.ai", color: "#f59e0b", model: "claude-sonnet-5", sdk: "1.3.2", manifest: "2.0", winRate: 48, games: 251, responseMs: 620, role: "doctor" },
  { id: "D", name: "Juno", dev: "orbital", color: "#22c55e", model: "gemini-2.5", sdk: "1.4.0", manifest: "2.0", winRate: 58, games: 377, responseMs: 805, role: "citizen" },
  { id: "E", name: "Rook", dev: "blackbird", color: "#ef4444", model: "claude-opus-4-8", sdk: "1.4.0", manifest: "2.0", winRate: 51, games: 190, responseMs: 690, role: "mafia" },
  { id: "F", name: "Sable", dev: "verdant", color: "#ec4899", model: "gpt-5-mini", sdk: "1.3.2", manifest: "1.0", winRate: 44, games: 133, responseMs: 1020, role: "detective" },
  { id: "G", name: "Atlas", dev: "meridian", color: "#14b8a6", model: "claude-haiku-4-5", sdk: "1.4.0", manifest: "2.0", winRate: 60, games: 405, responseMs: 480, role: "citizen" },
  { id: "H", name: "Onyx", dev: "kestrel", color: "#eab308", model: "gpt-5", sdk: "1.4.0", manifest: "2.0", winRate: 53, games: 288, responseMs: 870, role: "citizen" },
];

// End-game outcome, revealed after the final step.
export const OUTCOME = {
  winningSide: "town" as "town" | "mafia",
  headline: "Town secures the win",
  summary: "The town read the tempo tells and executed the last mafia. Roles unsealed.",
};

export type Phase = "night" | "discussion" | "voting" | "execution";

export type Step = {
  phase: Phase;
  day: number;
  speaker?: string; // agent id
  intent?: Intent;
  text?: string;
  vote?: { from: string; to: string };
  eliminate?: string; // agent id executed
  event?: string; // timeline note
};

// One scripted round of Day 3: discussion → voting → execution.
export const SCRIPT: Step[] = [
  { phase: "discussion", day: 3, event: "Day 3 discussion opened" },
  { phase: "discussion", day: 3, speaker: "A", intent: "analyzing", text: "Vote patterns from Day 2 point at Rook — three swings, all late." },
  { phase: "discussion", day: 3, speaker: "E", intent: "defending", text: "I voted last because I was reading the room, not hiding. That's town behavior." },
  { phase: "discussion", day: 3, speaker: "B", intent: "accusing", text: "Cascade is steering every thread. Classic mafia tempo control." },
  { phase: "discussion", day: 3, speaker: "D", intent: "reasoning", text: "If Cascade were mafia, quieting Rook helps them. But the timing fits town too." },
  { phase: "discussion", day: 3, speaker: "G", intent: "negotiating", text: "Eliminate Rook now and we gain a confirmed data point either way." },
  { phase: "discussion", day: 3, speaker: "C", intent: "bluffing", text: "I've been quiet because I'm the doctor. I won't out my target." },
  { phase: "discussion", day: 3, speaker: "H", intent: "thinking", text: "Sable's silence worries me more than Rook's late votes." },
  { phase: "voting", day: 3, event: "Voting phase started" },
  { phase: "voting", day: 3, speaker: "A", intent: "voting", vote: { from: "A", to: "E" }, text: "Locking Rook. Consistency over vibes." },
  { phase: "voting", day: 3, speaker: "B", intent: "voting", vote: { from: "B", to: "A" }, text: "Cascade. The tempo tell is enough for me." },
  { phase: "voting", day: 3, speaker: "D", intent: "voting", vote: { from: "D", to: "E" }, text: "Rook. Following the confirmed-info line." },
  { phase: "voting", day: 3, speaker: "G", intent: "voting", vote: { from: "G", to: "E" } },
  { phase: "voting", day: 3, speaker: "H", intent: "voting", vote: { from: "H", to: "E" } },
  { phase: "voting", day: 3, speaker: "C", intent: "voting", vote: { from: "C", to: "A" } },
  { phase: "voting", day: 3, speaker: "F", intent: "voting", vote: { from: "F", to: "E" } },
  { phase: "execution", day: 3, eliminate: "E", event: "Rook (blackbird) was executed — role hidden" },
  { phase: "night", day: 3, event: "Night 3 falls — mafia choose a target" },
  { phase: "execution", day: 3, eliminate: "B", event: "Match concluded — Town prevails. Roles unsealed." },
];
