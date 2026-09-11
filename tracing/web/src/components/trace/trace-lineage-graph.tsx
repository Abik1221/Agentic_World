"use client";

import { useCallback, useEffect, useMemo } from "react";
import {
  Background,
  Controls,
  Handle,
  MiniMap,
  Position,
  ReactFlow,
  useEdgesState,
  useNodesState,
  type Edge,
  type Node,
  type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import type { SpanNode } from "@/lib/pyyol-lens-api";
import { colorForSpanType, withAlpha } from "@/lib/span-colors";

const SYNTH_ROOT = "__pyyol_trace_root__";

function wrapRoots(nodes: SpanNode[]): SpanNode[] {
  if (nodes.length <= 1) return nodes;
  return [
    {
      span_id: SYNTH_ROOT,
      trace_id: nodes[0]?.trace_id ?? "",
      parent_span_id: "",
      span_type: "trace",
      step_name: "Run",
      status: "ok",
      started_at: "",
      ended_at: "",
      latency_ms: 0,
      provider: "",
      model: "",
      tool_name: "",
      total_tokens: 0,
      estimated_cost: 0,
      error_type: "",
      error_message: "",
      children: nodes,
    },
  ];
}

type LayoutRow = {
  id: string;
  label: string;
  status: string;
  spanType: string;
  latencyMs: number;
  depth: number;
  rowIndex: number;
};

function flattenLayout(tree: SpanNode[]): LayoutRow[] {
  const roots = wrapRoots(tree);
  const out: LayoutRow[] = [];
  let rowIndex = 0;

  const visit = (node: SpanNode, depth: number) => {
    const id = node.span_id;
    const rawLabel = node.step_name || node.span_type || id;
    const label = rawLabel.length > 44 ? `${rawLabel.slice(0, 42)}…` : rawLabel;
    out.push({
      id,
      label,
      status: node.status || "ok",
      spanType: node.span_type || "operation",
      latencyMs: node.latency_ms || 0,
      depth,
      rowIndex,
    });
    rowIndex += 1;
    for (const ch of node.children ?? []) {
      visit(ch, depth + 1);
    }
  };

  for (const r of roots) {
    visit(r, 0);
  }
  return out;
}

function buildEdges(tree: SpanNode[]): Edge[] {
  const edges: Edge[] = [];
  const roots = wrapRoots(tree);

  const visit = (node: SpanNode, parentId: string | undefined) => {
    if (parentId) {
      edges.push({
        id: `${parentId}->${node.span_id}`,
        source: parentId,
        target: node.span_id,
      });
    }
    for (const ch of node.children ?? []) {
      visit(ch, node.span_id);
    }
  };

  for (const r of roots) {
    for (const ch of r.children ?? []) {
      visit(ch, r.span_id);
    }
  }
  return edges;
}

function SpanFlowNode({ data, selected }: NodeProps) {
  const d = data as {
    label: string;
    meta: string;
    error?: boolean;
    fill: string;
    durationPct: number;
  };
  return (
    <div
      style={{
        padding: "10px 14px 8px",
        borderRadius: 10,
        border: selected ? "2px solid #fbbf24" : `1px solid ${d.error ? "#f87171" : d.fill}`,
        background: d.error ? "rgba(127,29,29,0.45)" : withAlpha(d.fill, 0.16),
        color: "#f8fafc",
        fontSize: 12,
        minWidth: 132,
        maxWidth: 228,
        boxShadow: selected ? "0 0 12px rgba(251,191,36,0.2)" : "0 2px 8px rgba(0,0,0,0.35)",
      }}
    >
      <Handle type="target" position={Position.Left} style={{ background: d.fill }} />
      <div style={{ fontWeight: 700, lineHeight: 1.25 }}>{d.label}</div>
      <div style={{ marginTop: 4, fontSize: 11, color: "#e2e8f0", fontFamily: "var(--font-mono)" }}>{d.meta}</div>
      <div
        aria-hidden
        style={{
          marginTop: 8,
          height: 4,
          borderRadius: 999,
          background: "rgba(15,23,42,0.55)",
          overflow: "hidden",
        }}
      >
        <div
          style={{
            width: `${Math.max(8, d.durationPct)}%`,
            height: "100%",
            background: d.fill,
          }}
        />
      </div>
      <Handle type="source" position={Position.Right} style={{ background: d.fill }} />
    </div>
  );
}

const nodeTypes = { span: SpanFlowNode };

export default function TraceLineageGraph({
  tree,
  selectedSpanId,
  onSelectSpan,
  executionOrder,
}: {
  tree: SpanNode[];
  selectedSpanId: string | null;
  onSelectSpan: (id: string | null) => void;
  executionOrder?: Map<string, number>;
}) {
  const rows = useMemo(() => flattenLayout(tree), [tree]);
  const initialEdges = useMemo(() => buildEdges(tree), [tree]);

  const toFlowNodes = useCallback(
    (selId: string | null): Node[] => {
      const xGap = 264;
      const yGap = 88;
      const maxLatency = Math.max(1, ...rows.map((r) => r.latencyMs || 0));
      return rows.map((n) => {
        const selected = n.id === selId;
        const err = n.status === "error";
        const ord = executionOrder?.get(n.id);
        const fill = colorForSpanType(n.spanType, n.status);
        return {
          id: n.id,
          type: "span",
          position: { x: 24 + n.depth * xGap, y: 20 + n.rowIndex * yGap },
          data: {
            label: n.label,
            meta: `${ord != null ? `#${ord} · ` : ""}${Math.round(n.latencyMs)}ms · ${n.spanType}`,
            error: err,
            fill,
            durationPct: Math.min(100, ((n.latencyMs || 0) / maxLatency) * 100),
          },
          selected,
        };
      });
    },
    [executionOrder, rows],
  );

  const [nodes, setNodes, onNodesChange] = useNodesState(toFlowNodes(selectedSpanId));
  const [edges, setEdges, onEdgesChange] = useEdgesState(initialEdges);

  useEffect(() => {
    setNodes(toFlowNodes(selectedSpanId));
  }, [selectedSpanId, setNodes, toFlowNodes]);

  useEffect(() => {
    setEdges(initialEdges);
  }, [initialEdges, setEdges]);

  const onNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      if (node.id === SYNTH_ROOT) return;
      onSelectSpan(node.id);
    },
    [onSelectSpan],
  );

  return (
    <div className="trace-lineage-flow">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onNodeClick={onNodeClick}
        fitView
        fitViewOptions={{ padding: 0.12 }}
        minZoom={0.15}
        maxZoom={1.5}
        proOptions={{ hideAttribution: true }}
      >
        <Background gap={22} color="#475569" />
        <Controls />
        <MiniMap
          style={{ background: "#0f172a", border: "1px solid #334155" }}
          nodeColor={(n) => {
            const fill = (n.data as { fill?: string } | undefined)?.fill;
            return fill || "#64748b";
          }}
          maskStrokeWidth={3}
          zoomable
          pannable
        />
      </ReactFlow>
    </div>
  );
}
