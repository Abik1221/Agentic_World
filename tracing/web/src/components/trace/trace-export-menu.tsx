"use client";

import { useCallback, useState } from "react";
import type { EventRow, SpanNode, TraceSummary } from "@/lib/pyyol-lens-api";
import {
  buildExportJson,
  buildExportMarkdown,
  downloadFile,
  sanitizeTraceFilename,
} from "@/lib/trace-export";

type Props = {
  trace: TraceSummary;
  displayTree: SpanNode[];
  events: EventRow[];
  traceCompleted: EventRow | null;
};

export default function TraceExportMenu({ trace, displayTree, events, traceCompleted }: Props) {
  const [open, setOpen] = useState(false);

  const onJson = useCallback(() => {
    const doc = buildExportJson({ trace, displayTree, events, traceCompleted });
    downloadFile(
      sanitizeTraceFilename(trace.trace_id, "json"),
      "application/json",
      JSON.stringify(doc, null, 2),
    );
    setOpen(false);
  }, [trace, displayTree, events, traceCompleted]);

  const onMd = useCallback(() => {
    const md = buildExportMarkdown({ trace, displayTree, events, traceCompleted });
    downloadFile(sanitizeTraceFilename(trace.trace_id, "md"), "text/markdown;charset=utf-8", md);
    setOpen(false);
  }, [trace, displayTree, events, traceCompleted]);

  return (
    <div className="trace-export">
      <button
        type="button"
        className="trace-export-trigger"
        aria-expanded={open}
        aria-haspopup="menu"
        onClick={() => setOpen((o) => !o)}
      >
        Export
        <span className="trace-export-chevron" aria-hidden>
          {open ? "▴" : "▾"}
        </span>
      </button>
      {open ? (
        <>
          <button
            type="button"
            className="trace-export-backdrop"
            aria-label="Close export menu"
            onClick={() => setOpen(false)}
          />
          <div className="trace-export-menu" role="menu">
            <button type="button" className="trace-export-item" role="menuitem" onClick={onJson}>
              Download JSON
              <span className="trace-export-hint">Hierarchical + full payloads</span>
            </button>
            <button type="button" className="trace-export-item" role="menuitem" onClick={onMd}>
              Download Markdown
              <span className="trace-export-hint">Human-readable; truncates large payloads</span>
            </button>
          </div>
        </>
      ) : null}
    </div>
  );
}
