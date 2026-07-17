/** Normalize telemetry payloads from pyyol-api and legacy ingest shapes. */

export type ChatMessage = { role?: string; content?: unknown };

export function normalizedPromptMessages(
  payload: Record<string, unknown>,
): ChatMessage[] | undefined {
  const raw = payload.input_messages ?? payload.messages;
  if (!Array.isArray(raw)) return undefined;
  return raw as ChatMessage[];
}

export function normalizedAssistantOutput(payload: Record<string, unknown>): unknown {
  return (
    payload.assistant_output_preview ??
    payload.response ??
    payload.output ??
    payload.output_text
  );
}

export type ParallelCall = { tool?: string; args?: unknown };

export function normalizedParallelCalls(
  payload: Record<string, unknown>,
): ParallelCall[] | undefined {
  const pc = payload.parallel_calls;
  if (!Array.isArray(pc)) return undefined;
  return pc as ParallelCall[];
}

export function normalizedUsage(payload: Record<string, unknown>): Record<string, unknown> | undefined {
  const u = payload.usage;
  if (u && typeof u === "object" && !Array.isArray(u)) return u as Record<string, unknown>;
  return undefined;
}

export function stringifyContent(content: unknown): string {
  if (typeof content === "string") return content;
  try {
    return JSON.stringify(content, null, 2);
  } catch {
    return String(content);
  }
}

export function normalizedToolFields(payload: Record<string, unknown>) {
  const rawResult =
    payload.tool_result ?? payload.result ?? payload.data_preview;
  let result: unknown = rawResult;
  if (typeof rawResult === 'string') {
    const t = rawResult.trim();
    if (t.startsWith('{') || t.startsWith('[')) {
      try {
        result = JSON.parse(t) as unknown;
      } catch {
        result = rawResult;
      }
    }
  }
  return {
    tool: (payload.tool ?? payload.tool_name) as string | undefined,
    args: payload.tool_args ?? payload.args,
    result,
    durationMs: payload.duration_ms as number | undefined,
    success: payload.success as boolean | undefined,
    error: payload.error as string | undefined,
    artifactIdsOut: coerceStringArray(payload.artifact_ids_out),
    archetype: payload.archetype as string | undefined,
    scope: payload.scope as string | undefined,
  };
}

export function coerceStringArray(v: unknown): string[] | undefined {
  if (!Array.isArray(v)) return undefined;
  const out = v.filter((x): x is string => typeof x === "string");
  return out.length ? out : undefined;
}

export function mergeArtifactIds(...sources: (string[] | undefined)[]): string[] {
  const set = new Set<string>();
  for (const arr of sources) {
    if (!arr) continue;
    for (const id of arr) {
      if (id) set.add(id);
    }
  }
  return [...set];
}
