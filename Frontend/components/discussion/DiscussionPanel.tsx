"use client";

// Shared, production-grade AI discussion panel used beside every game
// (Mafia · Monopoly · Goofspiel). Discord/Slack-grade behavior:
//   • fixed panel size — the page never shifts as messages arrive
//   • only the message area scrolls; smart auto-scroll with a 100px live zone
//   • "N new messages" pill when the spectator is reading history
//   • sticky header, consecutive-message grouping, hover actions, search,
//     intent filters, an Activity tab, a single shared typing indicator
//   • windowed rendering above a threshold so 10k+ messages stay smooth

import * as React from "react";
import { ArrowDown, ChevronDown, Copy, Radio, Search, SkipForward, User } from "lucide-react";
import { cn } from "@/lib/cn";

export type DiscussionIntent = { icon: string; label: string; color: string };
export type DiscussionStatus = { icon: string; label: string };

export type DiscussionMessage = {
  id: string | number;
  agentId: string;
  agentName: string;
  dev?: string;
  color: string;
  ts: string;
  text: string;
  intent?: DiscussionIntent; // doubles as the muted "state" badge + filter key
  extra?: React.ReactNode; // e.g. an inline trade chip
};

export type DiscussionActivity = { id: string | number; icon: string; text: string; ts: string };

// Preset live statuses (spec palette) — viewers map their per-game state to a key.
export const STATUS: Record<string, DiscussionStatus> = {
  thinking: { icon: "🧠", label: "Thinking" },
  speaking: { icon: "🎤", label: "Speaking" },
  voting: { icon: "🗳", label: "Voting" },
  analyzing: { icon: "📊", label: "Analyzing" },
  negotiating: { icon: "🤝", label: "Negotiating" },
  observing: { icon: "👀", label: "Observing" },
  waiting: { icon: "⏳", label: "Waiting" },
  idle: { icon: "⏳", label: "Idle" },
  dead: { icon: "💀", label: "Out" },
  winner: { icon: "👑", label: "Winner" },
};

const VIRT_THRESHOLD = 80; // window the list beyond this many messages
const EST_ROW = 92; // estimated row height for the windowed path
const LIVE_ZONE = 100; // px from bottom that counts as "live"

export function DiscussionPanel({
  title = "AI Discussion",
  messages,
  activity,
  typingNames = [],
  phaseLabel,
  live = true,
  statusOf,
  onJump,
  onProfile,
  className,
}: {
  title?: string;
  messages: DiscussionMessage[];
  activity?: DiscussionActivity[];
  typingNames?: string[];
  phaseLabel?: string;
  live?: boolean;
  statusOf?: (agentId: string) => DiscussionStatus | undefined;
  onJump?: (id: string | number) => void;
  onProfile?: (agentId: string) => void;
  className?: string;
}) {
  const [tab, setTab] = React.useState<"discussion" | "activity">("discussion");
  const [query, setQuery] = React.useState("");
  const [filter, setFilter] = React.useState<string>("all");
  const [searchOpen, setSearchOpen] = React.useState(false);

  // filter chips — "All" + the intents actually present
  const filters = React.useMemo(() => {
    const seen = new Map<string, DiscussionIntent>();
    for (const m of messages) if (m.intent && !seen.has(m.intent.label)) seen.set(m.intent.label, m.intent);
    return [{ key: "all", label: "All" }, ...Array.from(seen.values()).map((i) => ({ key: i.label, label: i.label }))];
  }, [messages]);

  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase();
    return messages.filter((m) => {
      if (filter !== "all" && m.intent?.label !== filter) return false;
      if (!q) return true;
      return (
        m.agentName.toLowerCase().includes(q) ||
        (m.dev ?? "").toLowerCase().includes(q) ||
        m.text.toLowerCase().includes(q) ||
        (m.intent?.label ?? "").toLowerCase().includes(q)
      );
    });
  }, [messages, query, filter]);

  return (
    <section className={cn("flex h-[440px] min-w-0 flex-col overflow-hidden rounded-xl border border-line bg-panel/80 shadow-[0_1px_2px_rgba(15,23,42,0.04),0_12px_30px_-18px_rgba(15,23,42,0.25)] backdrop-blur", className)}>
      {/* ── sticky header ── */}
      <div className="shrink-0 border-b border-line px-4 py-3">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Radio className="h-3.5 w-3.5 text-fg-muted" />
            <h3 className="text-[12px] font-semibold text-fg">{title}</h3>
            {live && (
              <span className="inline-flex items-center gap-1 rounded-full border border-red-500/30 bg-red-500/10 px-1.5 py-0.5 font-mono text-[9px] font-semibold uppercase tracking-wider text-red-400">
                <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-red-500" /> Live
              </span>
            )}
          </div>
          <button onClick={() => setSearchOpen((s) => !s)} className={cn("rounded-md p-1.5 text-fg-muted transition hover:bg-panel-2 hover:text-fg", searchOpen && "bg-panel-2 text-fg")}>
            <Search className="h-3.5 w-3.5" />
          </button>
        </div>
        <div className="mt-1 flex items-center gap-2 font-mono text-[10px] text-fg-muted">
          {phaseLabel && <span>{phaseLabel}</span>}
          {phaseLabel && <span className="h-2.5 w-px bg-line" />}
          <span>{messages.length.toLocaleString()} messages</span>
        </div>

        {searchOpen && (
          <div className="mt-2.5 flex items-center gap-2 rounded-lg border border-line bg-panel-2/50 px-2.5 py-1.5">
            <Search className="h-3.5 w-3.5 text-fg-muted" />
            <input
              autoFocus
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search agent, developer, keyword, intent…"
              className="w-full bg-transparent text-[12px] text-fg placeholder:text-fg-muted focus:outline-none"
            />
            {query && (
              <button onClick={() => setQuery("")} className="font-mono text-[10px] text-fg-muted hover:text-fg">
                clear
              </button>
            )}
          </div>
        )}

        {/* tabs + filters */}
        <div className="mt-2.5 flex items-center gap-1.5">
          <Tab active={tab === "discussion"} onClick={() => setTab("discussion")}>
            Discussion
          </Tab>
          {activity && (
            <Tab active={tab === "activity"} onClick={() => setTab("activity")}>
              Activity
            </Tab>
          )}
        </div>
        {tab === "discussion" && filters.length > 2 && (
          <div className="mt-2 flex flex-wrap gap-1.5">
            {filters.map((f) => (
              <button
                key={f.key}
                onClick={() => setFilter(f.key)}
                className={cn("rounded-full border px-2 py-0.5 text-[10px] font-medium transition", filter === f.key ? "border-brand/40 bg-brand/10 text-brand" : "border-line text-fg-muted hover:text-fg")}
              >
                {f.label}
              </button>
            ))}
          </div>
        )}
      </div>

      {/* ── scrollable body ── */}
      {tab === "discussion" ? (
        <MessageList messages={filtered} query={query} statusOf={statusOf} onJump={onJump} onProfile={onProfile} />
      ) : (
        <ActivityList activity={activity ?? []} />
      )}

      {/* ── fixed footer: single shared typing indicator ── */}
      <div className="flex h-8 shrink-0 items-center border-t border-line px-4">
        {typingNames.length > 0 ? <Typing names={typingNames} /> : <span className="font-mono text-[10px] text-fg-muted/60">Idle</span>}
      </div>
    </section>
  );
}

function Tab({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button onClick={onClick} className={cn("relative rounded-md px-2.5 py-1 text-[11px] font-semibold uppercase tracking-wider transition", active ? "text-fg" : "text-fg-muted hover:text-fg")}>
      {children}
      {active && <span className="absolute inset-x-1.5 -bottom-[9px] h-0.5 rounded-full bg-brand" />}
    </button>
  );
}

/* ───────────────────────── message list (scroll + windowing) ─────────────── */
function MessageList({
  messages,
  query,
  statusOf,
  onJump,
  onProfile,
}: {
  messages: DiscussionMessage[];
  query: string;
  statusOf?: (agentId: string) => DiscussionStatus | undefined;
  onJump?: (id: string | number) => void;
  onProfile?: (agentId: string) => void;
}) {
  const ref = React.useRef<HTMLDivElement>(null);
  const [stick, setStick] = React.useState(true);
  const [newCount, setNewCount] = React.useState(0);
  const seen = React.useRef(messages.length);
  const prevIds = React.useRef<Set<string | number>>(new Set());

  // ids added since last render → entrance animation for just those
  const freshIds = React.useMemo(() => {
    const fresh = new Set<string | number>();
    for (const m of messages) if (!prevIds.current.has(m.id)) fresh.add(m.id);
    prevIds.current = new Set(messages.map((m) => m.id));
    return fresh;
  }, [messages]);

  const measure = () => {
    const el = ref.current;
    if (!el) return el;
    const dist = el.scrollHeight - el.scrollTop - el.clientHeight;
    const atBottom = dist < LIVE_ZONE;
    setStick(atBottom);
    if (atBottom) {
      seen.current = messages.length;
      setNewCount(0);
    }
  };

  React.useEffect(() => {
    if (stick) {
      ref.current?.scrollTo({ top: ref.current.scrollHeight, behavior: "smooth" });
      seen.current = messages.length;
      setNewCount(0);
    } else {
      setNewCount(Math.max(0, messages.length - seen.current));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [messages.length]);

  const jump = () => {
    setStick(true);
    setNewCount(0);
    seen.current = messages.length;
    ref.current?.scrollTo({ top: ref.current.scrollHeight, behavior: "smooth" });
  };

  const grouped = (i: number) => i > 0 && messages[i - 1].agentId === messages[i].agentId;

  const virtual = messages.length > VIRT_THRESHOLD;

  return (
    <div className="relative min-h-0 flex-1">
      <div ref={ref} onScroll={measure} className="dsx-scroll h-full overflow-y-auto px-3 py-3">
        {messages.length === 0 ? (
          <p className="mt-8 text-center font-mono text-[11px] text-fg-muted">No messages match your search.</p>
        ) : virtual ? (
          <VirtualRows messages={messages} scrollRef={ref} grouped={grouped} query={query} statusOf={statusOf} onJump={onJump} onProfile={onProfile} />
        ) : (
          <div className="space-y-0.5">
            {messages.map((m, i) => (
              <Row key={m.id} m={m} grouped={grouped(i)} animate={freshIds.has(m.id)} query={query} status={statusOf?.(m.agentId)} onJump={onJump} onProfile={onProfile} />
            ))}
          </div>
        )}
      </div>

      {!stick && newCount > 0 && (
        <button onClick={jump} className="dsx-pill absolute bottom-3 left-1/2 flex items-center gap-1.5 rounded-full border border-brand/40 bg-panel px-3 py-1.5 text-[11px] font-semibold text-brand shadow-lg transition hover:bg-brand/10">
          <ChevronDown className="h-3.5 w-3.5" /> {newCount} new message{newCount > 1 ? "s" : ""}
        </button>
      )}
      {!stick && newCount === 0 && (
        <button onClick={jump} className="dsx-pill absolute bottom-3 right-3 flex items-center justify-center rounded-full border border-line bg-panel p-2 text-fg-muted shadow-lg transition hover:text-fg">
          <ArrowDown className="h-3.5 w-3.5" />
        </button>
      )}
    </div>
  );
}

/* windowed renderer for very large histories (measured heights, absolute layout) */
function VirtualRows({
  messages,
  scrollRef,
  grouped,
  query,
  statusOf,
  onJump,
  onProfile,
}: {
  messages: DiscussionMessage[];
  scrollRef: React.RefObject<HTMLDivElement>;
  grouped: (i: number) => boolean;
  query: string;
  statusOf?: (agentId: string) => DiscussionStatus | undefined;
  onJump?: (id: string | number) => void;
  onProfile?: (agentId: string) => void;
}) {
  const heights = React.useRef<number[]>([]);
  const [, force] = React.useReducer((x) => x + 1, 0);
  const [range, setRange] = React.useState({ start: 0, end: Math.min(messages.length, 30) });

  const offsets = React.useMemo(() => {
    const o: number[] = [0];
    for (let i = 0; i < messages.length; i++) o[i + 1] = o[i] + (heights.current[i] || EST_ROW);
    return o;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [messages.length, range]);
  const total = offsets[messages.length];

  const recompute = React.useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const top = el.scrollTop;
    const bottom = top + el.clientHeight;
    let start = 0;
    while (start < messages.length && offsets[start + 1] < top - 300) start++;
    let end = start;
    while (end < messages.length && offsets[end] < bottom + 300) end++;
    setRange({ start, end });
  }, [messages.length, offsets, scrollRef]);

  React.useEffect(() => {
    recompute();
    const el = scrollRef.current;
    if (!el) return;
    el.addEventListener("scroll", recompute);
    return () => el.removeEventListener("scroll", recompute);
  }, [recompute, scrollRef]);

  const setH = (i: number, h: number) => {
    if (Math.abs((heights.current[i] || EST_ROW) - h) > 1) {
      heights.current[i] = h;
      force();
    }
  };

  const rows = [];
  for (let i = range.start; i < range.end; i++) {
    const m = messages[i];
    rows.push(
      <div
        key={m.id}
        ref={(el) => {
          if (el) setH(i, el.offsetHeight);
        }}
        style={{ position: "absolute", top: offsets[i], left: 0, right: 0 }}
        className="px-0"
      >
        <Row m={m} grouped={grouped(i)} animate={false} query={query} status={statusOf?.(m.agentId)} onJump={onJump} onProfile={onProfile} />
      </div>,
    );
  }
  return (
    <div style={{ position: "relative", height: total }}>{rows}</div>
  );
}

/* ───────────────────────── one message row ─────────────────────── */
function Row({
  m,
  grouped,
  animate,
  query,
  status,
  onJump,
  onProfile,
}: {
  m: DiscussionMessage;
  grouped: boolean;
  animate: boolean;
  query: string;
  status?: DiscussionStatus;
  onJump?: (id: string | number) => void;
  onProfile?: (agentId: string) => void;
}) {
  const [copied, setCopied] = React.useState(false);
  const copy = () => {
    navigator.clipboard?.writeText(m.text).then(
      () => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1200);
      },
      () => {},
    );
  };
  return (
    <div className={cn("group relative rounded-lg px-2 py-1 transition-colors hover:bg-panel-2/40", animate && "dsx-in", grouped ? "mt-0.5" : "mt-2.5")}>
      {/* hover actions */}
      <div className="absolute right-1.5 top-1 z-10 hidden items-center gap-0.5 rounded-md border border-line bg-panel/95 px-0.5 py-0.5 shadow-sm backdrop-blur group-hover:flex">
        <IconBtn title={copied ? "Copied" : "Copy"} onClick={copy}>
          <Copy className="h-3 w-3" />
        </IconBtn>
        {onJump && (
          <IconBtn title="Jump to replay" onClick={() => onJump(m.id)}>
            <SkipForward className="h-3 w-3" />
          </IconBtn>
        )}
        {onProfile && (
          <IconBtn title="Agent profile" onClick={() => onProfile(m.agentId)}>
            <User className="h-3 w-3" />
          </IconBtn>
        )}
        <span className="px-1 font-mono text-[9px] text-fg-muted">{m.ts}</span>
      </div>

      {grouped ? (
        <div className="flex gap-2">
          <span className="w-6 shrink-0 text-right font-mono text-[9px] leading-5 text-transparent group-hover:text-fg-muted">{m.ts}</span>
          <div className="min-w-0 flex-1">
            {m.intent && <StateBadge intent={m.intent} />}
            <p className="text-[13px] leading-relaxed text-fg">{highlight(m.text, query)}</p>
            {m.extra}
          </div>
        </div>
      ) : (
        <>
          <div className="mb-1 flex items-center gap-2">
            <button onClick={() => onProfile?.(m.agentId)} className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full border text-[10px] font-bold" style={{ borderColor: m.color, color: m.color }}>
              {m.agentName[0]}
            </button>
            <span className="text-[12px] font-semibold text-fg">{highlight(m.agentName, query)}</span>
            {m.dev && <span className="font-mono text-[10px] text-fg-muted">{m.dev}</span>}
            {status && (
              <span className="inline-flex items-center gap-1 rounded-full bg-panel-2 px-1.5 py-0.5 font-mono text-[9px] text-fg-muted">
                <span>{status.icon}</span>
                {status.label}
              </span>
            )}
            <span className="ml-auto font-mono text-[10px] text-fg-muted">{m.ts}</span>
          </div>
          <div className="ml-8">
            {m.intent && <StateBadge intent={m.intent} />}
            <p className="text-[13px] leading-relaxed text-fg">{highlight(m.text, query)}</p>
            {m.extra}
          </div>
        </>
      )}
    </div>
  );
}

function StateBadge({ intent }: { intent: DiscussionIntent }) {
  return (
    <div className="mb-1 inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 font-mono text-[9px] uppercase tracking-wide" style={{ background: `${intent.color}14`, color: intent.color }}>
      <span>{intent.icon}</span>
      {intent.label}
    </div>
  );
}

function IconBtn({ title, onClick, children }: { title: string; onClick: () => void; children: React.ReactNode }) {
  return (
    <button title={title} onClick={onClick} className="rounded p-1 text-fg-muted transition hover:bg-panel-2 hover:text-fg">
      {children}
    </button>
  );
}

function highlight(text: string, query: string): React.ReactNode {
  const q = query.trim();
  if (!q) return text;
  const i = text.toLowerCase().indexOf(q.toLowerCase());
  if (i < 0) return text;
  return (
    <>
      {text.slice(0, i)}
      <mark className="rounded bg-brand/25 px-0.5 text-fg">{text.slice(i, i + q.length)}</mark>
      {text.slice(i + q.length)}
    </>
  );
}

/* ───────────────────────── activity tab ─────────────────────── */
function ActivityList({ activity }: { activity: DiscussionActivity[] }) {
  const ref = React.useRef<HTMLDivElement>(null);
  const [stick, setStick] = React.useState(true);
  const onScroll = () => {
    const el = ref.current;
    if (!el) return;
    setStick(el.scrollHeight - el.scrollTop - el.clientHeight < LIVE_ZONE);
  };
  React.useEffect(() => {
    if (stick) ref.current?.scrollTo({ top: ref.current.scrollHeight, behavior: "smooth" });
  }, [activity.length, stick]);
  return (
    <div ref={ref} onScroll={onScroll} className="dsx-scroll min-h-0 flex-1 space-y-1.5 overflow-y-auto px-3 py-3">
      {activity.length === 0 ? (
        <p className="mt-8 text-center font-mono text-[11px] text-fg-muted">No system events yet.</p>
      ) : (
        activity.map((a) => (
          <div key={a.id} className="dsx-in flex items-center gap-2.5 rounded-lg border border-line bg-panel-2/30 px-3 py-2">
            <span className="text-[14px]">{a.icon}</span>
            <span className="flex-1 text-[12px] text-fg">{a.text}</span>
            <span className="shrink-0 font-mono text-[10px] text-fg-muted">{a.ts}</span>
          </div>
        ))
      )}
    </div>
  );
}

/* ───────────────────────── shared typing indicator ─────────────────────── */
function Typing({ names }: { names: string[] }) {
  const label = names.length === 1 ? `${names[0]} is thinking` : names.length === 2 ? `${names[0]} and ${names[1]} are thinking` : `${names.length} agents are thinking`;
  return (
    <div className="flex items-center gap-2 text-[11px] text-fg-muted">
      <span>{label}</span>
      <span className="flex gap-1">
        {[0, 1, 2].map((i) => (
          <span key={i} className="dsx-typing h-1.5 w-1.5 rounded-full bg-fg-muted" style={{ animationDelay: `${i * 200}ms` }} />
        ))}
      </span>
    </div>
  );
}
