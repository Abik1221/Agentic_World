import { getJson, maxLeadContextTokens, runId } from "./lib";

type KPI = {
  lead_model_calls_prompt_series?: Array<{
    prompt_tokens: number;
    index?: number;
  }>;
};

/**
 * Asserts no model_call_completed in the lead series exceeds MAX_LEAD_CONTEXT_TOKENS
 * (reads /v1/runs/:runId/telemetry-kpi).
 */
export async function run(): Promise<void> {
  const id = runId();
  if (!id) {
    throw new Error("set RUN_ID for eval-lead-context-size");
  }
  const kpi = await getJson<KPI>(`/v1/runs/${encodeURIComponent(id)}/telemetry-kpi`);
  const cap = maxLeadContextTokens();
  const series = kpi.lead_model_calls_prompt_series || [];
  for (const row of series) {
    if (row.prompt_tokens > cap) {
      throw new Error(
        `lead prompt_tokens ${row.prompt_tokens} > MAX_LEAD_CONTEXT_TOKENS ${cap} (index ${row.index})`,
      );
    }
  }
  console.log(
    "eval-lead-context-size: ok",
    `(checked ${series.length} lead model_call points, cap=${cap})`,
  );
}
