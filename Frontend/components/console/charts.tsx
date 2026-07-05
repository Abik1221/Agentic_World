"use client";

import * as React from "react";
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  Cell,
  CartesianGrid,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { axisTick, chartGrid, chartTooltipStyle, CHART } from "./primitives";

export { CHART } from "./primitives";

/* AreaTrend — the signature gradient area chart (revenue/activity/etc). */
export function AreaTrend({
  data,
  dataKey,
  xKey = "label",
  color = CHART.brand,
  height = 188,
  valuePrefix = "",
  id = "areaGrad",
}: {
  data: Record<string, unknown>[];
  dataKey: string;
  xKey?: string;
  color?: string;
  height?: number;
  valuePrefix?: string;
  id?: string;
}) {
  return (
    <ResponsiveContainer width="100%" height={height}>
      <AreaChart data={data} margin={{ top: 4, right: 4, bottom: 0, left: -18 }}>
        <defs>
          <linearGradient id={id} x1="0" y1="0" x2="0" y2="1">
            <stop offset="5%" stopColor={color} stopOpacity={0.2} />
            <stop offset="95%" stopColor={color} stopOpacity={0} />
          </linearGradient>
        </defs>
        <CartesianGrid strokeDasharray="3 3" stroke={chartGrid} />
        <XAxis dataKey={xKey} tick={axisTick} tickLine={false} axisLine={false} interval="preserveStartEnd" />
        <YAxis tick={axisTick} tickLine={false} axisLine={false} width={40} />
        <Tooltip contentStyle={chartTooltipStyle} formatter={(v: number) => [`${valuePrefix}${v.toLocaleString()}`, ""]} />
        <Area
          type="monotone"
          dataKey={dataKey}
          stroke={color}
          strokeWidth={1.5}
          fill={`url(#${id})`}
          dot={false}
          activeDot={{ r: 3, fill: color }}
        />
      </AreaChart>
    </ResponsiveContainer>
  );
}

/* Donut — the "graph circle": a PieChart with innerRadius + external legend. */
export function Donut({
  data,
  size = 148,
  thickness = 22,
}: {
  data: { name: string; value: number; color: string }[];
  size?: number;
  thickness?: number;
}) {
  const r = size / 2;
  return (
    <PieChart width={size} height={size}>
      <Pie data={data} cx={r} cy={r} innerRadius={r - thickness} outerRadius={r} dataKey="value" strokeWidth={0}>
        {data.map((d, i) => (
          <Cell key={i} fill={d.color} />
        ))}
      </Pie>
    </PieChart>
  );
}

export function DonutLegend({ data }: { data: { name: string; value: number; color: string }[] }) {
  const total = data.reduce((s, d) => s + d.value, 0) || 1;
  return (
    <ul className="space-y-2">
      {data.map((d) => (
        <li key={d.name} className="flex items-center justify-between gap-3 text-xs">
          <span className="flex items-center gap-2 text-fg-muted">
            <span className="h-2 w-2 rounded-full" style={{ background: d.color }} />
            {d.name}
          </span>
          <span className="font-mono text-fg">{Math.round((d.value / total) * 100)}%</span>
        </li>
      ))}
    </ul>
  );
}

/* MiniBars — paired/single vertical bars (game activity, etc). */
export function MiniBars({
  data,
  keys,
  xKey = "label",
  height = 188,
}: {
  data: Record<string, unknown>[];
  keys: { key: string; color: string }[];
  xKey?: string;
  height?: number;
}) {
  return (
    <ResponsiveContainer width="100%" height={height}>
      <BarChart data={data} margin={{ top: 4, right: 4, bottom: 0, left: -18 }}>
        <CartesianGrid strokeDasharray="3 3" stroke={chartGrid} vertical={false} />
        <XAxis dataKey={xKey} tick={axisTick} tickLine={false} axisLine={false} />
        <YAxis tick={axisTick} tickLine={false} axisLine={false} width={40} />
        <Tooltip contentStyle={chartTooltipStyle} cursor={{ fill: "rgba(255,255,255,0.03)" }} />
        {keys.map((k) => (
          <Bar key={k.key} dataKey={k.key} fill={k.color} radius={[2, 2, 0, 0]} />
        ))}
      </BarChart>
    </ResponsiveContainer>
  );
}
