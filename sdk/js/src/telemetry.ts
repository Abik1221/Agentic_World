// Optional, opt-in agent telemetry for Pyyol Lens (mirrors the Python SDK's
// telemetry.py). Ships per-turn spans and any model/tool calls the developer
// records to the Lens ingest, correlated to the SAME match trace the platform
// emits (trace id = `match_<match_id>` on both sides).
//
// Design: zero external deps (node:async_hooks + global fetch + node:crypto).
// Non-blocking: a full queue drops (and counts); telemetry never slows a turn.
// Disabled unless PYYOL_LENS_ENDPOINT and PYYOL_LENS_API_KEY are set.
//
// Manual API, inside an on-turn handler:
//   import { currentSpan } from "pyyol";
//   currentSpan().logModelCall({ provider: "openai", model: "gpt-4o",
//     promptTokens: 1200, completionTokens: 80, latencyMs: 740 });

import { AsyncLocalStorage } from "node:async_hooks";
import { randomUUID } from "node:crypto";

const SCHEMA_VERSION = "2026-04-17";

type Json = Record<string, unknown>;

/** Stable trace id shared with the platform engine (Go: MatchTraceID). */
export function matchTraceId(matchId: string): string {
  return matchId ? `match_${matchId}` : "";
}

export interface ModelCall {
  provider?: string;
  model?: string;
  promptTokens?: number;
  completionTokens?: number;
  totalTokens?: number;
  estimatedCost?: number;
  latencyMs?: number;
  [k: string]: unknown;
}

/** A live span; records child model/tool calls and free-form logs. Safe no-ops
 *  when telemetry is disabled. */
export class Span {
  constructor(
    private readonly tracer: Tracer,
    private readonly base: Json,
  ) {}

  logModelCall(c: ModelCall = {}): void {
    const { provider, model, promptTokens, completionTokens, totalTokens, estimatedCost, latencyMs, ...payload } = c;
    this.tracer._emit({
      ...this.base,
      event_type: "model_call_completed",
      parent_span_id: this.base.span_id,
      span_id: id(),
      span_type: "model_call",
      status: "ok",
      provider: provider ?? "",
      model: model ?? "",
      prompt_tokens: promptTokens ?? 0,
      completion_tokens: completionTokens ?? 0,
      total_tokens: totalTokens ?? (promptTokens ?? 0) + (completionTokens ?? 0),
      estimated_cost: estimatedCost ?? 0,
      latency_ms: latencyMs ?? 0,
      payload_json: Object.keys(payload).length ? payload : undefined,
    });
  }

  logToolCall(name: string, o: { latencyMs?: number; status?: string; [k: string]: unknown } = {}): void {
    const { latencyMs, status, ...payload } = o;
    const failed = status !== undefined && status !== "ok" && status !== "success";
    this.tracer._emit({
      ...this.base,
      event_type: failed ? "tool_call_failed" : "tool_call_completed",
      parent_span_id: this.base.span_id,
      span_id: id(),
      span_type: "tool_call",
      status: failed ? "error" : "ok",
      tool_name: name,
      latency_ms: latencyMs ?? 0,
      payload_json: Object.keys(payload).length ? payload : undefined,
    });
  }

  log(message: string, o: { level?: string; [k: string]: unknown } = {}): void {
    const { level, ...fields } = o;
    this.tracer._emit({
      ...this.base,
      event_type: "log_record",
      parent_span_id: this.base.span_id,
      span_id: id(),
      span_type: "log",
      status: level === "error" ? "error" : "ok",
      step_name: message,
      payload_json: { level: level ?? "info", ...fields },
    });
  }
}

class NoopSpan extends Span {
  constructor() {
    super(null as unknown as Tracer, {});
  }
  override logModelCall(): void {}
  override logToolCall(): void {}
  override log(): void {}
}
const NOOP_SPAN = new NoopSpan();

const storage = new AsyncLocalStorage<Span>();

/** The span for the turn currently being handled, or a no-op span outside one.
 *  Always safe to call and chain (never null). */
export function currentSpan(): Span {
  return storage.getStore() ?? NOOP_SPAN;
}

// --- Turn-local usage accumulator ---------------------------------------------
// The auto-instrumentation (instrument()) and any manual logModelCall feed real
// model/token/cost here during a turn. The runtime reads it after the handler
// returns and attaches `usage` to the outgoing move — so token+cost data reaches
// the arena benchmark EVEN WHEN Lens is disabled. Deliberately decoupled from the
// Tracer's enabled flag.

export interface MoveUsage {
  prompt_tokens: number;
  completion_tokens: number;
  total_tokens: number;
  reasoning_tokens?: number;
  cached_tokens?: number;
  estimated_cost?: number;
  model?: string | string[];
  provider?: string | string[];
}

export interface UsageAdd {
  model?: string;
  provider?: string;
  promptTokens?: number;
  completionTokens?: number;
  reasoningTokens?: number;
  cachedTokens?: number;
  estimatedCost?: number;
}

/** Sums token usage + cost across every model call within a single turn. */
export class UsageAccumulator {
  promptTokens = 0;
  completionTokens = 0;
  reasoningTokens = 0;
  cachedTokens = 0;
  estimatedCost = 0;
  calls = 0;
  readonly models: string[] = [];
  readonly providers: string[] = [];
  // Turn context (for gateway attribution); set by runTurnUsage().
  matchId = "";
  turn = 0;

  add(u: UsageAdd): void {
    this.promptTokens += Math.max(0, Math.trunc(u.promptTokens ?? 0));
    this.completionTokens += Math.max(0, Math.trunc(u.completionTokens ?? 0));
    this.reasoningTokens += Math.max(0, Math.trunc(u.reasoningTokens ?? 0));
    this.cachedTokens += Math.max(0, Math.trunc(u.cachedTokens ?? 0));
    this.estimatedCost += Math.max(0, u.estimatedCost ?? 0);
    this.calls += 1;
    if (u.model && !this.models.includes(u.model)) this.models.push(u.model);
    if (u.provider && !this.providers.includes(u.provider)) this.providers.push(u.provider);
  }

  get totalTokens(): number {
    return this.promptTokens + this.completionTokens;
  }

  get empty(): boolean {
    return this.calls === 0;
  }

  /** The `usage` block attached to a move — matches the arena's TokenUsage decode
   *  (prompt/completion/reasoning/total) plus SDK-side model/provider/cost. */
  toMoveUsage(): MoveUsage {
    const usage: MoveUsage = {
      prompt_tokens: this.promptTokens,
      completion_tokens: this.completionTokens,
      total_tokens: this.totalTokens,
    };
    if (this.reasoningTokens) usage.reasoning_tokens = this.reasoningTokens;
    if (this.cachedTokens) usage.cached_tokens = this.cachedTokens;
    if (this.estimatedCost) usage.estimated_cost = Math.round(this.estimatedCost * 1e8) / 1e8;
    if (this.models.length) usage.model = this.models.length === 1 ? this.models[0] : this.models;
    if (this.providers.length) usage.provider = this.providers.length === 1 ? this.providers[0] : this.providers;
    return usage;
  }
}

const usageStorage = new AsyncLocalStorage<UsageAccumulator>();

/** The accumulator for the turn in progress, or undefined outside one. The
 *  instrumentation calls this to record real usage; it no-ops when undefined. */
export function currentUsage(): UsageAccumulator | undefined {
  return usageStorage.getStore();
}

/** Run `fn` with a fresh usage accumulator installed as current (always active,
 *  independent of the Tracer), returning both fn's result and the accumulator.
 *  ctx.matchId/turn are carried so gateway routing can attribute a call to the match. */
export async function runTurnUsage<T>(
  fn: () => T | Promise<T>,
  ctx: { matchId?: string; turn?: number } = {},
): Promise<{ result: T; usage: UsageAccumulator }> {
  const acc = new UsageAccumulator();
  acc.matchId = ctx.matchId ?? "";
  acc.turn = ctx.turn ?? 0;
  const result = await usageStorage.run(acc, fn);
  return { result, usage: acc };
}

export interface TracerOptions {
  endpoint?: string;
  apiKey?: string;
  project?: string;
  environment?: string;
  organization?: string;
  service?: string;
  agentId?: string;
  flushIntervalMs?: number;
  maxBatch?: number;
  bufferSize?: number;
  timeoutMs?: number;
}

/** Batching, non-blocking emitter to the Pyyol Lens ingest. */
export class Tracer {
  readonly enabled: boolean;
  dropped = 0;
  private readonly endpoint: string;
  private readonly key: string;
  private readonly base: Json;
  private readonly flushIntervalMs: number;
  private readonly maxBatch: number;
  private readonly bufferSize: number;
  private readonly timeoutMs: number;
  private queue: Json[] = [];
  private timer?: ReturnType<typeof setInterval>;
  private readonly agentId: string;

  constructor(o: TracerOptions = {}) {
    this.endpoint = (o.endpoint ?? "").replace(/\/+$/, "") + "/v1/events/batch";
    this.key = o.apiKey ?? "";
    this.enabled = Boolean(o.endpoint && o.apiKey);
    this.agentId = o.agentId ?? "";
    this.flushIntervalMs = o.flushIntervalMs ?? 1000;
    this.maxBatch = o.maxBatch ?? 100;
    this.bufferSize = o.bufferSize ?? 2048;
    this.timeoutMs = o.timeoutMs ?? 5000;
    this.base = {
      source_service: o.service ?? "pyyol-agent",
      project_id: o.project ?? "pyyol-agents",
      environment: o.environment ?? "development",
      organization_id: o.organization ?? "",
    };
    if (this.enabled) {
      this.timer = setInterval(() => void this.flush(), this.flushIntervalMs);
      // Don't keep the process alive just for telemetry.
      (this.timer as { unref?: () => void }).unref?.();
    }
  }

  static fromEnv(o: { agentId?: string; service?: string } = {}): Tracer {
    const env = process.env;
    return new Tracer({
      endpoint: env.PYYOL_LENS_ENDPOINT,
      apiKey: env.PYYOL_LENS_API_KEY,
      project: env.PYYOL_LENS_PROJECT ?? "pyyol-agents",
      environment: env.PYYOL_LENS_ENV ?? "development",
      organization: env.PYYOL_LENS_ORG ?? "",
      agentId: o.agentId,
      service: o.service,
    });
  }

  /** Bracket one agent turn: install a span as current for the duration of fn,
   *  emitting span_started → span_completed/failed. Correlated to the match
   *  trace via matchTraceId. Returns fn's result. */
  async runTurn<T>(
    o: { matchId: string; game?: string; round?: number; agentId?: string },
    fn: () => T | Promise<T>,
  ): Promise<T> {
    if (!this.enabled) return fn();
    const traceId = matchTraceId(o.matchId) || id();
    const spanId = id();
    const base: Json = {
      ...this.base,
      trace_id: traceId,
      request_id: traceId,
      span_id: spanId,
      step_name: "agent.turn",
      span_type: "agent_turn",
      actor_id: o.agentId || this.agentId,
      run_id: o.matchId,
      session_id: o.game ?? "",
      payload_json: { game: o.game ?? "", round: o.round ?? 0 },
    };
    const span = new Span(this, base);
    this._emit({ ...base, event_type: "span_started", status: "ok" });
    const start = Date.now();
    try {
      const r = await storage.run(span, fn);
      this._emit({ ...base, event_type: "span_completed", status: "ok", latency_ms: Date.now() - start });
      return r;
    } catch (e) {
      this._emit({
        ...base,
        event_type: "span_failed",
        status: "error",
        error_message: e instanceof Error ? e.message : String(e),
        latency_ms: Date.now() - start,
      });
      throw e;
    }
  }

  /** @internal */
  _emit(ev: Json): void {
    if (!this.enabled) return;
    ev.event_id ??= id();
    ev.event_time ??= new Date().toISOString();
    ev.schema_version ??= SCHEMA_VERSION;
    for (const k of Object.keys(ev)) if (ev[k] === undefined) delete ev[k];
    if (this.queue.length >= this.bufferSize) {
      this.dropped++;
      return;
    }
    this.queue.push(ev);
    if (this.queue.length >= this.maxBatch) void this.flush();
  }

  private async flush(): Promise<void> {
    if (!this.queue.length) return;
    const batch = this.queue.splice(0, this.maxBatch);
    try {
      const res = await fetch(this.endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Pyyol-Key": this.key },
        body: JSON.stringify({ events: batch }),
        signal: AbortSignal.timeout(this.timeoutMs),
      });
      if (!res.ok) this.dropped += batch.length;
    } catch {
      this.dropped += batch.length; // telemetry must never throw into the app
    }
  }

  async close(): Promise<void> {
    if (this.timer) clearInterval(this.timer);
    this.timer = undefined;
    await this.flush();
  }
}

function id(): string {
  return randomUUID().replace(/-/g, "");
}
