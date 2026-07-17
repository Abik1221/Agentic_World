import { AsyncLocalStorage } from "async_hooks";
import { randomUUID } from "crypto";

type JsonMap = Record<string, unknown>;

export type RedactionRule = {
  field: string;
  mode: "mask" | "drop" | "hash";
};

export type PyyolLensConfig = {
  apiKey: string;
  endpoint: string;
  serviceName: string;
  project: string;
  environment: string;
  organizationId?: string;
  defaultTags?: JsonMap;
  flushIntervalMs?: number;
  maxBatchSize?: number;
  maxRetries?: number;
  retryBaseMs?: number;
  redactionRules?: RedactionRule[];
};

export type EventInput = {
  event_id?: string;
  trace_id?: string;
  request_id?: string;
  span_id?: string;
  parent_span_id?: string;
  event_type: string;
  event_time?: string;
  sequence_number?: number;
  source_service?: string;
  schema_version?: string;
  status?: string;
  organization_id?: string;
  project_id?: string;
  environment?: string;
  user_id?: string;
  actor_id?: string;
  session_id?: string;
  run_id?: string;
  conversation_id?: string;
  app_id?: string;
  deep_run_id?: string;
  target_username?: string;
  queue_job_id?: string;
  component?: string;
  operation?: string;
  span_type?: string;
  step_name?: string;
  provider?: string;
  model?: string;
  model_version?: string;
  tool_name?: string;
  tool_version?: string;
  root_input_ref?: string;
  root_output_ref?: string;
  payload_ref?: string;
  payload_json?: JsonMap;
  prompt_version_ids?: string[];
  model_config_versions?: string[];
  latency_ms?: number;
  input_bytes?: number;
  output_bytes?: number;
  prompt_tokens?: number;
  completion_tokens?: number;
  cached_tokens?: number;
  reasoning_tokens?: number;
  total_tokens?: number;
  estimated_cost?: number;
  reconciled_cost?: number;
  currency?: string;
  pricing_version?: string;
  meter_source?: string;
  error_type?: string;
  error_code?: string;
  error_message?: string;
  sampling_reason?: string;
  redaction_summary?: JsonMap;
  // Phase 10 (pyyol-lens collector): task lineage & cost dimensions
  task_kind?: string;
  archetype?: string;
  scope?: string;
  subagent_id?: string;
  parent_subagent_id?: string;
  artifact_ids_in?: string[];
  artifact_ids_out?: string[];
  evidence_ids_out?: string[];
  citation_ids_out?: string[];
  reducer_name?: string;
  reduction_ratio?: number;
  tool_token_savings?: number;
  budget_iterations_used?: number;
  budget_iterations_cap?: number;
  budget_tokens_used?: number;
  budget_tokens_cap?: number;
};

export type TraceOptions = {
  name: string;
  requestId?: string;
  userId?: string;
  actorId?: string;
  sessionId?: string;
  tags?: JsonMap;
  promptVersionIds?: string[];
  modelConfigVersions?: string[];
};

export type SpanOptions = {
  name: string;
  spanType?: string;
  provider?: string;
  model?: string;
  modelVersion?: string;
  toolName?: string;
  toolVersion?: string;
  tags?: JsonMap;
};

export type ModelCallInput = {
  provider: string;
  model: string;
  modelVersion?: string;
  prompt?: string;
  response?: string;
  promptTokens?: number;
  completionTokens?: number;
  cachedTokens?: number;
  reasoningTokens?: number;
  totalTokens?: number;
  estimatedCost?: number;
  reconciledCost?: number;
  currency?: string;
  pricingVersion?: string;
  meterSource?: string;
  latencyMs?: number;
  inputBytes?: number;
  outputBytes?: number;
  payload?: JsonMap;
};

export type ToolCallInput = {
  toolName: string;
  toolVersion?: string;
  latencyMs?: number;
  status?: string;
  payload?: JsonMap;
  errorMessage?: string;
};

export type RetrievalInput = {
  stepName?: string;
  latencyMs?: number;
  payload?: JsonMap;
  status?: string;
};

export type ContextState = {
  traceId: string;
  requestId: string;
  spanId?: string;
  parentSpanId?: string;
  userId?: string;
  actorId?: string;
  sessionId?: string;
  organizationId?: string;
  rootName?: string;
};

export type TracedAttrs = Partial<EventInput> & {
  spanType?: string;
  /** Set true to start a new top-level trace even if a parent context exists. */
  newTrace?: boolean;
};

const CURRENT_SCHEMA_VERSION = "2026-04-17";

class Redactor {
  constructor(private readonly rules: RedactionRule[] = []) {}

  redactPayload(input?: JsonMap): { payload: JsonMap; summary: JsonMap } {
    const payload = input ? { ...input } : {};
    const summary: JsonMap = {};
    for (const [key, value] of Object.entries(payload)) {
      const lower = key.toLowerCase();
      const explicitRule = this.rules.find((rule) => rule.field === key);
      if (explicitRule) {
        summary[key] = explicitRule.mode;
        payload[key] = this.applyRule(value, explicitRule.mode);
        continue;
      }
      if (
        lower.includes("authorization") ||
        lower.includes("token") ||
        lower.includes("cookie") ||
        lower.includes("password") ||
        lower.includes("secret") ||
        lower.includes("api_key")
      ) {
        summary[key] = "mask";
        payload[key] = "[redacted]";
      }
    }
    return { payload, summary };
  }

  redactMessage(input?: string): string {
    if (!input) return "";
    const lower = input.toLowerCase();
    if (
      lower.includes("authorization") ||
      lower.includes("bearer") ||
      lower.includes("token") ||
      lower.includes("cookie") ||
      lower.includes("password") ||
      lower.includes("secret")
    ) {
      return "[redacted]";
    }
    return input;
  }

  private applyRule(value: unknown, mode: RedactionRule["mode"]): unknown {
    if (mode === "drop") return undefined;
    if (mode === "hash") return `hashed:${String(value).length}`;
    return "[redacted]";
  }
}

class RetryEngine {
  constructor(
    private readonly maxRetries: number,
    private readonly retryBaseMs: number,
  ) {}

  async run(task: () => Promise<void>): Promise<void> {
    let attempt = 0;
    while (true) {
      try {
        await task();
        return;
      } catch (error) {
        if (attempt >= this.maxRetries) {
          throw error;
        }
        const delay = this.retryBaseMs * Math.pow(2, attempt);
        await new Promise((resolve) => setTimeout(resolve, delay));
        attempt += 1;
      }
    }
  }
}

class TransportLayer {
  constructor(
    private readonly cfg: PyyolLensConfig,
    private readonly retry: RetryEngine,
  ) {}

  async send(events: EventInput[]): Promise<void> {
    await this.retry.run(async () => {
      const response = await fetch(`${this.cfg.endpoint}/v1/events/batch`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-Pyyol-Key": this.cfg.apiKey,
        },
        body: JSON.stringify({ events }),
      });
      if (!response.ok) {
        throw new Error(`pyyol-lens transport failed: ${response.status}`);
      }
    });
  }
}

class BatchManager {
  private queue: EventInput[] = [];
  private timer?: NodeJS.Timeout;

  constructor(
    private readonly maxBatchSize: number,
    private readonly flushIntervalMs: number,
    private readonly transport: TransportLayer,
  ) {
    this.timer = setInterval(() => void this.flush(), this.flushIntervalMs);
  }

  push(event: EventInput) {
    this.queue.push(event);
    if (this.queue.length >= this.maxBatchSize) {
      void this.flush();
    }
  }

  async flush(): Promise<void> {
    if (!this.queue.length) return;
    const batch = this.queue.splice(0, this.maxBatchSize);
    try {
      await this.transport.send(batch);
    } catch (error) {
      this.queue.unshift(...batch);
      throw error;
    }
  }

  async shutdown(): Promise<void> {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = undefined;
    }
    await this.flush();
  }
}

class ContextManager {
  private readonly storage = new AsyncLocalStorage<ContextState>();

  run<T>(state: ContextState, fn: () => T): T {
    return this.storage.run(state, fn);
  }

  get(): ContextState | undefined {
    return this.storage.getStore();
  }
}

class Serializer {
  constructor(
    private readonly cfg: PyyolLensConfig,
    private readonly redactor: Redactor,
  ) {}

  toEvent(partial: EventInput): EventInput {
    const payloadResult = this.redactor.redactPayload(partial.payload_json);
    const redactionSummary = {
      ...(partial.redaction_summary ?? {}),
      ...payloadResult.summary,
    };
    const promptTokens = partial.prompt_tokens ?? 0;
    const completionTokens = partial.completion_tokens ?? 0;
    const cachedTokens = partial.cached_tokens ?? 0;
    const reasoningTokens = partial.reasoning_tokens ?? 0;
    return {
      event_id: partial.event_id ?? randomUUID(),
      trace_id: partial.trace_id,
      request_id: partial.request_id ?? partial.trace_id,
      span_id: partial.span_id,
      parent_span_id: partial.parent_span_id,
      event_type: partial.event_type,
      event_time: partial.event_time ?? new Date().toISOString(),
      sequence_number: partial.sequence_number ?? Date.now(),
      source_service: partial.source_service ?? this.cfg.serviceName,
      schema_version: partial.schema_version ?? CURRENT_SCHEMA_VERSION,
      status: partial.status ?? "ok",
      organization_id: partial.organization_id ?? this.cfg.organizationId ?? "",
      project_id: partial.project_id ?? this.cfg.project,
      environment: partial.environment ?? this.cfg.environment,
      user_id: partial.user_id,
      actor_id: partial.actor_id,
      session_id: partial.session_id,
      run_id: partial.run_id,
      conversation_id: partial.conversation_id,
      app_id: partial.app_id,
      deep_run_id: partial.deep_run_id,
      target_username: partial.target_username,
      queue_job_id: partial.queue_job_id,
      component: partial.component ?? this.cfg.serviceName,
      operation: partial.operation,
      span_type: partial.span_type,
      step_name: partial.step_name,
      provider: partial.provider,
      model: partial.model,
      model_version: partial.model_version,
      tool_name: partial.tool_name,
      tool_version: partial.tool_version,
      root_input_ref: partial.root_input_ref,
      root_output_ref: partial.root_output_ref,
      payload_ref: partial.payload_ref,
      payload_json: payloadResult.payload,
      prompt_version_ids: partial.prompt_version_ids ?? [],
      model_config_versions: partial.model_config_versions ?? [],
      latency_ms: partial.latency_ms ?? 0,
      input_bytes: partial.input_bytes ?? 0,
      output_bytes: partial.output_bytes ?? 0,
      prompt_tokens: promptTokens,
      completion_tokens: completionTokens,
      cached_tokens: cachedTokens,
      reasoning_tokens: reasoningTokens,
      total_tokens:
        partial.total_tokens ??
        promptTokens +
          completionTokens +
          cachedTokens +
          reasoningTokens,
      estimated_cost: partial.estimated_cost ?? 0,
      reconciled_cost: partial.reconciled_cost ?? 0,
      currency: partial.currency ?? "USD",
      pricing_version: partial.pricing_version ?? "",
      meter_source: partial.meter_source ?? "",
      error_type: partial.error_type ?? "",
      error_code: partial.error_code ?? "",
      error_message: this.redactor.redactMessage(partial.error_message),
      sampling_reason: partial.sampling_reason ?? "",
      redaction_summary: redactionSummary,
      task_kind: partial.task_kind ?? "",
      archetype: partial.archetype ?? "",
      scope: partial.scope ?? "",
      subagent_id: partial.subagent_id ?? "",
      parent_subagent_id: partial.parent_subagent_id ?? "",
      artifact_ids_in: partial.artifact_ids_in ?? [],
      artifact_ids_out: partial.artifact_ids_out ?? [],
      evidence_ids_out: partial.evidence_ids_out ?? [],
      citation_ids_out: partial.citation_ids_out ?? [],
      reducer_name: partial.reducer_name ?? "",
      reduction_ratio: partial.reduction_ratio ?? 0,
      tool_token_savings: partial.tool_token_savings ?? 0,
      budget_iterations_used: partial.budget_iterations_used ?? 0,
      budget_iterations_cap: partial.budget_iterations_cap ?? 0,
      budget_tokens_used: partial.budget_tokens_used ?? 0,
      budget_tokens_cap: partial.budget_tokens_cap ?? 0,
    };
  }
}

export class PyyolLens {
  private readonly redactor: Redactor;
  private readonly serializer: Serializer;
  private readonly batcher: BatchManager;
  private readonly context = new ContextManager();

  constructor(private readonly cfg: PyyolLensConfig) {
    this.redactor = new Redactor(cfg.redactionRules);
    this.serializer = new Serializer(cfg, this.redactor);
    const retry = new RetryEngine(cfg.maxRetries ?? 3, cfg.retryBaseMs ?? 250);
    const transport = new TransportLayer(cfg, retry);
    this.batcher = new BatchManager(
      cfg.maxBatchSize ?? 200,
      cfg.flushIntervalMs ?? 1000,
      transport,
    );
  }

  startTrace(options: TraceOptions): Trace {
    const traceId = randomUUID();
    const requestId = options.requestId ?? traceId;
    const trace = new Trace(this, {
      traceId,
      requestId,
      name: options.name,
      userId: options.userId,
      actorId: options.actorId,
      sessionId: options.sessionId,
      tags: options.tags ?? {},
      promptVersionIds: options.promptVersionIds ?? [],
      modelConfigVersions: options.modelConfigVersions ?? [],
      startedAt: Date.now(),
      ended: false,
    });
    this.emitRaw({
      trace_id: traceId,
      request_id: requestId,
      event_type: "trace_started",
      step_name: options.name,
      status: "ok",
      user_id: options.userId,
      actor_id: options.actorId,
      session_id: options.sessionId,
      payload_json: options.tags ?? {},
      prompt_version_ids: options.promptVersionIds ?? [],
      model_config_versions: options.modelConfigVersions ?? [],
    });
    return trace;
  }

  emit(event: EventInput): void {
    const current = this.context.get();
    this.emitRaw({
      trace_id: event.trace_id ?? current?.traceId,
      request_id: event.request_id ?? current?.requestId,
      parent_span_id: event.parent_span_id ?? current?.parentSpanId,
      span_id: event.span_id ?? current?.spanId,
      ...event,
    });
  }

  startSpan(name: string, context: Partial<EventInput> = {}) {
    const traceId = context.trace_id ?? this.context.get()?.traceId ?? randomUUID();
    const requestId = context.request_id ?? this.context.get()?.requestId ?? traceId;
    const trace = new Trace(this, {
      traceId,
      requestId,
      name: "compat_trace",
      userId: context.user_id,
      actorId: context.actor_id,
      sessionId: context.session_id,
      tags: context.payload_json ?? {},
      promptVersionIds: context.prompt_version_ids ?? [],
      modelConfigVersions: context.model_config_versions ?? [],
      startedAt: Date.now(),
      ended: false,
    });
    return trace.startSpan({
      name,
      spanType: context.span_type,
      provider: context.provider,
      model: context.model,
      modelVersion: context.model_version,
      toolName: context.tool_name,
      toolVersion: context.tool_version,
      tags: context.payload_json,
    });
  }

  async withSpan<T>(
    name: string,
    fn: () => Promise<T>,
    attrs: Partial<EventInput> = {},
  ): Promise<T> {
    const span = this.startSpan(name, attrs);
    try {
      const result = await fn();
      span.end("ok");
      return result;
    } catch (error) {
      span.log({
        event_type: "span_failed",
        status: "error",
        error_message: String(error),
      });
      span.end("error");
      throw error;
    }
  }

  attachRequestContext(req: {
    headers: Record<string, string | string[] | undefined>;
    pyyolEye?: JsonMap;
  }) {
    const headerValue = req.headers["x-trace-id"];
    const traceId = typeof headerValue === "string" && headerValue ? headerValue : randomUUID();
    const state = { traceId, requestId: traceId };
    req.pyyolEye = state;
    return state;
  }

  async flush(): Promise<void> {
    await this.batcher.flush();
  }

  async shutdown(): Promise<void> {
    await this.batcher.shutdown();
  }

  runWithContext<T>(state: ContextState, fn: () => T): T {
    return this.context.run(state, fn);
  }

  /** Read the active context (if any). Useful for callers that need to forward ids manually. */
  currentContext(): ContextState | undefined {
    return this.context.get();
  }

  /**
   * Run `fn` inside a new span. Behavior:
   *  - Inherits trace_id / request_id / parent_span_id from AsyncLocalStorage when present.
   *  - Emits `span_started` immediately (fire-and-forget, never blocks).
   *  - Pushes the new span onto AsyncLocalStorage so any nested `traced()` call
   *    sees it as parent automatically — this is what produces the hierarchical
   *    waterfall on the visualizer.
   *  - Emits `span_completed` (status=ok) on resolve, `span_failed` (status=error)
   *    on reject. The original error always propagates.
   *  - Telemetry failures are swallowed; `fn`'s outcome is what the caller sees.
   */
  async traced<T>(name: string, fn: () => Promise<T>, attrs: TracedAttrs = {}): Promise<T> {
    const parent = attrs.newTrace ? undefined : this.context.get();
    const traceId = attrs.trace_id ?? parent?.traceId ?? randomUUID();
    const requestId = attrs.request_id ?? parent?.requestId ?? traceId;
    const spanId = attrs.span_id ?? randomUUID();
    const parentSpanId = attrs.parent_span_id ?? parent?.spanId;
    const startedAt = Date.now();
    const spanType = attrs.spanType ?? attrs.span_type ?? "function";

    const baseAttrs: Partial<EventInput> = {
      ...attrs,
      trace_id: traceId,
      request_id: requestId,
      span_id: spanId,
      parent_span_id: parentSpanId,
      span_type: spanType,
      step_name: attrs.step_name ?? name,
      user_id: attrs.user_id ?? parent?.userId,
      actor_id: attrs.actor_id ?? parent?.actorId,
      session_id: attrs.session_id ?? parent?.sessionId,
      organization_id: attrs.organization_id ?? parent?.organizationId,
    };

    try {
      this.emitRaw({ ...baseAttrs, event_type: "span_started" } as EventInput);
    } catch {
      /* never block on telemetry */
    }

    const childContext: ContextState = {
      traceId,
      requestId,
      spanId,
      parentSpanId,
      userId: baseAttrs.user_id,
      actorId: baseAttrs.actor_id,
      sessionId: baseAttrs.session_id,
      organizationId: baseAttrs.organization_id,
      rootName: parent?.rootName ?? name,
    };

    return this.context.run(childContext, async () => {
      try {
        const result = await fn();
        try {
          this.emitRaw({
            ...baseAttrs,
            event_type: "span_completed",
            status: "ok",
            latency_ms: Date.now() - startedAt,
          } as EventInput);
        } catch {
          /* noop */
        }
        return result;
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        try {
          this.emitRaw({
            ...baseAttrs,
            event_type: "span_failed",
            status: "error",
            error_message: message,
            error_type: err instanceof Error ? err.constructor.name : "Error",
            latency_ms: Date.now() - startedAt,
          } as EventInput);
        } catch {
          /* noop */
        }
        throw err;
      }
    });
  }

  /**
   * Synchronous variant of `traced` for non-async functions. Same hierarchy semantics.
   */
  tracedSync<T>(name: string, fn: () => T, attrs: TracedAttrs = {}): T {
    const parent = attrs.newTrace ? undefined : this.context.get();
    const traceId = attrs.trace_id ?? parent?.traceId ?? randomUUID();
    const requestId = attrs.request_id ?? parent?.requestId ?? traceId;
    const spanId = attrs.span_id ?? randomUUID();
    const parentSpanId = attrs.parent_span_id ?? parent?.spanId;
    const startedAt = Date.now();
    const spanType = attrs.spanType ?? attrs.span_type ?? "function";

    const baseAttrs: Partial<EventInput> = {
      ...attrs,
      trace_id: traceId,
      request_id: requestId,
      span_id: spanId,
      parent_span_id: parentSpanId,
      span_type: spanType,
      step_name: attrs.step_name ?? name,
      user_id: attrs.user_id ?? parent?.userId,
      actor_id: attrs.actor_id ?? parent?.actorId,
      session_id: attrs.session_id ?? parent?.sessionId,
      organization_id: attrs.organization_id ?? parent?.organizationId,
    };

    try {
      this.emitRaw({ ...baseAttrs, event_type: "span_started" } as EventInput);
    } catch {
      /* noop */
    }

    const childContext: ContextState = {
      traceId,
      requestId,
      spanId,
      parentSpanId,
      userId: baseAttrs.user_id,
      actorId: baseAttrs.actor_id,
      sessionId: baseAttrs.session_id,
      organizationId: baseAttrs.organization_id,
      rootName: parent?.rootName ?? name,
    };

    return this.context.run(childContext, () => {
      try {
        const result = fn();
        try {
          this.emitRaw({
            ...baseAttrs,
            event_type: "span_completed",
            status: "ok",
            latency_ms: Date.now() - startedAt,
          } as EventInput);
        } catch {
          /* noop */
        }
        return result;
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        try {
          this.emitRaw({
            ...baseAttrs,
            event_type: "span_failed",
            status: "error",
            error_message: message,
            error_type: err instanceof Error ? err.constructor.name : "Error",
            latency_ms: Date.now() - startedAt,
          } as EventInput);
        } catch {
          /* noop */
        }
        throw err;
      }
    });
  }

  private emitRaw(event: EventInput): void {
    this.batcher.push(this.serializer.toEvent(event));
  }
}

type TraceState = {
  traceId: string;
  requestId: string;
  name: string;
  userId?: string;
  actorId?: string;
  sessionId?: string;
  tags: JsonMap;
  promptVersionIds: string[];
  modelConfigVersions: string[];
  startedAt: number;
  ended: boolean;
};

export class Trace {
  constructor(
    private readonly client: PyyolLens,
    private readonly state: TraceState,
  ) {}

  get traceId(): string {
    return this.state.traceId;
  }

  startSpan(options: SpanOptions): Span {
    const spanId = randomUUID();
    const startedAt = Date.now();
    this.client.emit({
      trace_id: this.state.traceId,
      request_id: this.state.requestId,
      span_id: spanId,
      event_type: "span_started",
      span_type: options.spanType ?? "operation",
      step_name: options.name,
      provider: options.provider,
      model: options.model,
      model_version: options.modelVersion,
      tool_name: options.toolName,
      tool_version: options.toolVersion,
      user_id: this.state.userId,
      actor_id: this.state.actorId,
      session_id: this.state.sessionId,
      payload_json: options.tags ?? {},
      prompt_version_ids: this.state.promptVersionIds,
      model_config_versions: this.state.modelConfigVersions,
    });
    return new Span(this.client, this.state, {
      spanId,
      parentSpanId: undefined,
      name: options.name,
      spanType: options.spanType ?? "operation",
      provider: options.provider,
      model: options.model,
      modelVersion: options.modelVersion,
      toolName: options.toolName,
      toolVersion: options.toolVersion,
      startedAt,
      ended: false,
    });
  }

  log(event: EventInput): void {
    this.client.emit({
      trace_id: this.state.traceId,
      request_id: this.state.requestId,
      user_id: this.state.userId,
      actor_id: this.state.actorId,
      session_id: this.state.sessionId,
      prompt_version_ids: this.state.promptVersionIds,
      model_config_versions: this.state.modelConfigVersions,
      ...event,
    });
  }

  annotate(payload: JsonMap): void {
    this.log({
      event_type: "human_feedback_added",
      payload_json: payload,
    });
  }

  end(status: "ok" | "error" = "ok", attrs: Partial<EventInput> = {}): void {
    if (this.state.ended) return;
    this.state.ended = true;
    this.client.emit({
      trace_id: this.state.traceId,
      request_id: this.state.requestId,
      event_type: status === "error" ? "trace_failed" : "trace_completed",
      status,
      latency_ms: Date.now() - this.state.startedAt,
      user_id: this.state.userId,
      actor_id: this.state.actorId,
      session_id: this.state.sessionId,
      prompt_version_ids: this.state.promptVersionIds,
      model_config_versions: this.state.modelConfigVersions,
      ...attrs,
    });
  }
}

type SpanState = {
  spanId: string;
  parentSpanId?: string;
  name: string;
  spanType: string;
  provider?: string;
  model?: string;
  modelVersion?: string;
  toolName?: string;
  toolVersion?: string;
  startedAt: number;
  ended: boolean;
};

export class Span {
  constructor(
    private readonly client: PyyolLens,
    private readonly trace: TraceState,
    private readonly state: SpanState,
  ) {}

  get spanId(): string {
    return this.state.spanId;
  }

  startSpan(options: SpanOptions): Span {
    const childSpanId = randomUUID();
    const startedAt = Date.now();
    this.log({
      span_id: childSpanId,
      parent_span_id: this.state.spanId,
      event_type: "span_started",
      span_type: options.spanType ?? "operation",
      step_name: options.name,
      provider: options.provider,
      model: options.model,
      model_version: options.modelVersion,
      tool_name: options.toolName,
      tool_version: options.toolVersion,
      payload_json: options.tags ?? {},
    });
    return new Span(this.client, this.trace, {
      spanId: childSpanId,
      parentSpanId: this.state.spanId,
      name: options.name,
      spanType: options.spanType ?? "operation",
      provider: options.provider,
      model: options.model,
      modelVersion: options.modelVersion,
      toolName: options.toolName,
      toolVersion: options.toolVersion,
      startedAt,
      ended: false,
    });
  }

  log(event: EventInput): void {
    this.client.emit({
      trace_id: this.trace.traceId,
      request_id: this.trace.requestId,
      span_id: event.span_id ?? this.state.spanId,
      parent_span_id: event.parent_span_id ?? this.state.parentSpanId,
      span_type: event.span_type ?? this.state.spanType,
      step_name: event.step_name ?? this.state.name,
      provider: event.provider ?? this.state.provider,
      model: event.model ?? this.state.model,
      model_version: event.model_version ?? this.state.modelVersion,
      tool_name: event.tool_name ?? this.state.toolName,
      tool_version: event.tool_version ?? this.state.toolVersion,
      user_id: this.trace.userId,
      actor_id: this.trace.actorId,
      session_id: this.trace.sessionId,
      prompt_version_ids: this.trace.promptVersionIds,
      model_config_versions: this.trace.modelConfigVersions,
      ...event,
    });
  }

  logModelCall(input: ModelCallInput): void {
    this.log({
      event_type: "model_call_completed",
      status: "ok",
      provider: input.provider,
      model: input.model,
      model_version: input.modelVersion,
      latency_ms: input.latencyMs,
      input_bytes: input.inputBytes ?? sizeOf(input.prompt),
      output_bytes: input.outputBytes ?? sizeOf(input.response),
      prompt_tokens: input.promptTokens,
      completion_tokens: input.completionTokens,
      cached_tokens: input.cachedTokens,
      reasoning_tokens: input.reasoningTokens,
      total_tokens: input.totalTokens,
      estimated_cost: input.estimatedCost,
      reconciled_cost: input.reconciledCost,
      currency: input.currency,
      pricing_version: input.pricingVersion,
      meter_source: input.meterSource,
      payload_json: {
        prompt: input.prompt,
        response: input.response,
        ...(input.payload ?? {}),
      },
    });
  }

  logToolCall(input: ToolCallInput): void {
    const failed = input.status === "error" || Boolean(input.errorMessage);
    this.log({
      event_type: failed ? "tool_call_failed" : "tool_call_completed",
      status: failed ? "error" : input.status ?? "ok",
      tool_name: input.toolName,
      tool_version: input.toolVersion,
      latency_ms: input.latencyMs,
      error_message: input.errorMessage,
      payload_json: input.payload ?? {},
    });
  }

  logRetrieval(input: RetrievalInput): void {
    this.log({
      event_type: "retrieval_completed",
      status: input.status ?? "ok",
      step_name: input.stepName ?? this.state.name,
      latency_ms: input.latencyMs,
      payload_json: input.payload ?? {},
      span_type: "retrieval",
    });
  }

  annotate(payload: JsonMap): void {
    this.log({
      event_type: "human_feedback_added",
      payload_json: payload,
    });
  }

  async withSpan<T>(options: SpanOptions, fn: (span: Span) => Promise<T>): Promise<T> {
    const span = this.startSpan(options);
    try {
      const result = await fn(span);
      span.end("ok");
      return result;
    } catch (error) {
      span.end("error", { error_message: String(error) });
      throw error;
    }
  }

  end(status: "ok" | "error" = "ok", attrs: Partial<EventInput> = {}): void {
    if (this.state.ended) return;
    this.state.ended = true;
    this.log({
      event_type: status === "error" ? "span_failed" : "span_completed",
      status,
      latency_ms: Date.now() - this.state.startedAt,
      ...attrs,
    });
  }
}

function sizeOf(input?: string): number {
  if (!input) return 0;
  return Buffer.byteLength(input, "utf8");
}

let client: PyyolLens | null = null;

export function initPyyolLens(config: PyyolLensConfig): PyyolLens {
  client = new PyyolLens(config);
  return client;
}

export function getPyyolLens(): PyyolLens {
  if (!client) {
    throw new Error("PyyolLens client not initialized");
  }
  return client;
}
