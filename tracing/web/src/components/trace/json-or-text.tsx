"use client";

import { useMemo, useState } from "react";

const monoStyle: React.CSSProperties = {
  margin: 0,
  padding: 10,
  borderRadius: 8,
  background: "var(--wb-surface)",
  border: "1px solid var(--wb-border)",
  fontSize: 12,
  color: "var(--wb-text)",
  overflowX: "auto",
  whiteSpace: "pre-wrap",
  wordBreak: "break-word",
  fontFamily: "var(--font-mono), ui-monospace, SFMono-Regular, monospace",
};

const treeStyle: React.CSSProperties = {
  ...monoStyle,
  whiteSpace: "normal",
};

/** Strip an optional ```json fenced block so values pasted from LLM outputs
 *  still parse. */
function stripFences(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed.startsWith("```")) return trimmed;
  return trimmed
    .replace(/^```(?:json|jsonc)?\s*/i, "")
    .replace(/```\s*$/, "")
    .trim();
}

/** Try to parse a string as JSON (object or array). Strings that aren't
 *  JSON, or are scalars (numbers / quoted strings), are NOT treated as
 *  "viewable JSON" — we render those as plain text. */
function tryParseJsonObject(raw: unknown): unknown | undefined {
  if (typeof raw !== "string") return undefined;
  const stripped = stripFences(raw);
  if (!stripped) return undefined;
  const c = stripped[0];
  if (c !== "{" && c !== "[") return undefined;
  try {
    const parsed = JSON.parse(stripped);
    if (parsed && (typeof parsed === "object" || Array.isArray(parsed))) {
      return parsed;
    }
  } catch {
    /* not JSON — fall through */
  }
  return undefined;
}

/**
 * Render any value that might be JSON. Strategy:
 *  - if `value` is already an object/array → JSON tree viewer
 *  - if `value` is a string that parses to JSON (with/without ``` fences) → tree viewer
 *  - otherwise → monospace text with a "show full" toggle when truncated
 *
 * `previewChars` is the soft preview cap for plain text. When `previewChars`
 * is `0`, plain text is never truncated (user can still collapse JSON tree).
 */
export function JsonOrText({
  value,
  previewChars = 280,
  alwaysExpanded = false,
}: {
  value: unknown;
  previewChars?: number;
  alwaysExpanded?: boolean;
}) {
  const [expanded, setExpanded] = useState(alwaysExpanded);

  const treeData = useMemo(() => {
    if (value && typeof value === "object") return value;
    return tryParseJsonObject(value);
  }, [value]);

  if (treeData !== undefined) {
    return (
      <div style={treeStyle}>
        <JsonNode data={treeData} initialOpen depth={0} />
      </div>
    );
  }

  const text =
    typeof value === "string"
      ? value
      : value == null
        ? ""
        : (() => {
            try {
              return JSON.stringify(value);
            } catch {
              return String(value);
            }
          })();

  if (!text) {
    return (
      <pre style={monoStyle}>
        <span style={{ color: "var(--wb-muted-strong)" }}>(empty)</span>
      </pre>
    );
  }

  const overflows = previewChars > 0 && text.length > previewChars;
  const display =
    previewChars <= 0 || expanded || !overflows
      ? text
      : `${text.slice(0, previewChars)}…`;

  return (
    <div>
      <pre style={monoStyle}>{display}</pre>
      {overflows && (
        <button
          type="button"
          className="wb-link-btn"
          onClick={() => setExpanded((x) => !x)}
          style={{ marginTop: 6 }}
        >
          {expanded
            ? `Hide full (${text.length.toLocaleString()} chars)`
            : `Show full (${text.length.toLocaleString()} chars)`}
        </button>
      )}
    </div>
  );
}

/** One level of the JSON tree. Objects/arrays show keys; scalars render
 *  inline with a tiny type-hinted color. Long string scalars are
 *  collapsible so a single big field doesn't dominate the viewport. */
function JsonNode({
  data,
  initialOpen,
  depth,
  parentKey,
}: {
  data: unknown;
  initialOpen: boolean;
  depth: number;
  parentKey?: string;
}) {
  const isArray = Array.isArray(data);
  const isObject = !isArray && data !== null && typeof data === "object";

  if (!isArray && !isObject) {
    return <JsonScalar value={data} />;
  }

  const entries = isArray
    ? (data as unknown[]).map((v, i) => [String(i), v] as const)
    : Object.entries(data as Record<string, unknown>);
  const count = entries.length;

  // Auto-collapse big nodes past depth 0 so the inspector stays readable.
  const autoOpen = depth < 1 || (initialOpen && count <= 12);

  return (
    <JsonCollapse
      summary={
        <span>
          <span style={{ color: "var(--wb-muted-strong)" }}>
            {isArray ? `[${count}]` : `{${count}}`}
          </span>
          {parentKey != null && (
            <span style={{ color: "var(--wb-muted-strong)", marginLeft: 6 }}>
              {parentKey}
            </span>
          )}
        </span>
      }
      defaultOpen={autoOpen}
    >
      <ul
        style={{
          listStyle: "none",
          padding: "0 0 0 14px",
          margin: "4px 0 0 0",
          borderLeft: "1px solid var(--wb-border)",
        }}
      >
        {entries.map(([k, v]) => (
          <li key={k} style={{ padding: "2px 0" }}>
            {Array.isArray(v) || (v && typeof v === "object") ? (
              <JsonNode
                data={v}
                initialOpen={false}
                depth={depth + 1}
                parentKey={k}
              />
            ) : (
              <span>
                <span style={{ color: "#7dd3fc" }}>{k}</span>
                <span style={{ color: "var(--wb-muted-strong)" }}>: </span>
                <JsonScalar value={v} />
              </span>
            )}
          </li>
        ))}
      </ul>
    </JsonCollapse>
  );
}

function JsonCollapse({
  summary,
  defaultOpen,
  children,
}: {
  summary: React.ReactNode;
  defaultOpen: boolean;
  children: React.ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen((x) => !x)}
        style={{
          background: "transparent",
          border: 0,
          padding: 0,
          margin: 0,
          color: "inherit",
          font: "inherit",
          cursor: "pointer",
          textAlign: "left",
        }}
      >
        <span
          style={{
            display: "inline-block",
            width: 12,
            color: "var(--wb-muted-strong)",
          }}
        >
          {open ? "▾" : "▸"}
        </span>
        {summary}
      </button>
      {open && children}
    </div>
  );
}

/** Render a JSON scalar with type-hinted color. Long strings get a small
 *  inline expand control so they don't blow up the layout. */
function JsonScalar({ value }: { value: unknown }) {
  const [expanded, setExpanded] = useState(false);
  if (value === null) {
    return <span style={{ color: "#f87171" }}>null</span>;
  }
  if (typeof value === "boolean") {
    return <span style={{ color: "#fbbf24" }}>{String(value)}</span>;
  }
  if (typeof value === "number") {
    return <span style={{ color: "#a7f3d0" }}>{value}</span>;
  }
  const s = typeof value === "string" ? value : String(value);
  const CAP = 4000;
  if (s.length <= CAP || expanded) {
    return (
      <span style={{ color: "#f1f5f9", whiteSpace: "pre-wrap", wordBreak: "break-word" }}>
        {JSON.stringify(s)}
        {s.length > CAP && (
          <button
            type="button"
            onClick={() => setExpanded(false)}
            className="wb-link-btn"
            style={{ marginLeft: 8 }}
          >
            collapse
          </button>
        )}
      </span>
    );
  }
  return (
    <span style={{ color: "#f1f5f9" }}>
      {JSON.stringify(`${s.slice(0, CAP)}…`)}
      <button
        type="button"
        onClick={() => setExpanded(true)}
        className="wb-link-btn"
        style={{ marginLeft: 8 }}
      >
        show ({s.length.toLocaleString()} chars)
      </button>
    </span>
  );
}

/**
 * Reusable collapsible <section> wrapper for span-inspector groups. Native
 * <details> would also work, but we want consistent styling with the rest
 * of the workbench and the ability to default certain sections open or
 * closed (errors open, raw events closed).
 */
export function CollapsibleSection({
  title,
  badge,
  defaultOpen = true,
  children,
}: {
  title: string;
  badge?: React.ReactNode;
  defaultOpen?: boolean;
  children: React.ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <section>
      <button
        type="button"
        onClick={() => setOpen((x) => !x)}
        className="wb-eyebrow wb-collapsible-trigger"
        style={{
          background: "transparent",
          border: 0,
          padding: 0,
          margin: 0,
          font: "inherit",
          cursor: "pointer",
          display: "flex",
          alignItems: "center",
          gap: 6,
        }}
      >
        <span style={{ display: "inline-block", width: 12 }}>
          {open ? "▾" : "▸"}
        </span>
        <span>{title}</span>
        {badge != null && (
          <span style={{ marginLeft: 6, opacity: 0.7 }}>{badge}</span>
        )}
      </button>
      {open && <div style={{ marginTop: 8 }}>{children}</div>}
    </section>
  );
}
