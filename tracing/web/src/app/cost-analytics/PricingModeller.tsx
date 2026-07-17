"use client";

import { useState } from "react";

const etb = (n: number) =>
  `${(n ?? 0).toLocaleString("en-US", { maximumFractionDigits: 0 })} ETB`;
const usd = (n: number, dp = 4) =>
  `$${(n ?? 0).toLocaleString("en-US", {
    minimumFractionDigits: dp,
    maximumFractionDigits: dp,
  })}`;
const pct = (n: number) => `${(n * 100).toFixed(1)}%`;

function Lever({
  label,
  value,
  onChange,
  step,
}: {
  label: string;
  value: number;
  onChange: (n: number) => void;
  step: number;
}) {
  return (
    <label style={{ display: "block" }}>
      <span style={{ fontSize: 12, color: "#9a9a9a" }}>{label}</span>
      <input
        type="number"
        value={value}
        step={step}
        onChange={(e) => onChange(Number(e.target.value) || 0)}
        style={{
          marginTop: 4,
          width: "100%",
          padding: "8px 10px",
          borderRadius: 6,
          border: "1px solid var(--border, #232323)",
          background: "var(--bg, #0d0d0d)",
          color: "#fff",
          fontSize: 14,
        }}
      />
    </label>
  );
}

function Card({
  title,
  primary,
  primarySub,
  rows,
  warn,
}: {
  title: string;
  primary: string;
  primarySub?: string;
  rows: [string, string][];
  warn?: boolean;
}) {
  return (
    <div
      style={{
        border: "1px solid var(--border, #232323)",
        borderRadius: 10,
        padding: 16,
        background: "var(--bg, #0d0d0d)",
      }}
    >
      <div style={{ fontSize: 11, textTransform: "uppercase", letterSpacing: 0.5, color: "#8a8a8a" }}>
        {title}
      </div>
      <div style={{ marginTop: 4, fontSize: 24, fontWeight: 600, color: warn ? "#f59e0b" : "#fff" }}>
        {primary}
      </div>
      {primarySub && <div style={{ fontSize: 12, color: "#8a8a8a" }}>{primarySub}</div>}
      <div style={{ marginTop: 12, display: "grid", gap: 4 }}>
        {rows.map(([k, v]) => (
          <div key={k} style={{ display: "flex", justifyContent: "space-between", fontSize: 13 }}>
            <span style={{ color: "#9a9a9a" }}>{k}</span>
            <span style={{ color: "#e5e5e5" }}>{v}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

export default function PricingModeller({
  blendedUsdPer1M,
  avgTokensPerRun,
}: {
  blendedUsdPer1M: number;
  avgTokensPerRun: number;
}) {
  const [fx, setFx] = useState(150); // ETB per USD
  const [subRate, setSubRate] = useState(900); // y: subscription ETB / 1M
  const [odRate, setOdRate] = useState(1200); // x: on-demand ETB / 1M

  const costEtbPer1M = blendedUsdPer1M * fx;
  const subMarginPct = subRate ? (subRate - costEtbPer1M) / subRate : 0;
  const odMarginPct = odRate ? (odRate - costEtbPer1M) / odRate : 0;
  const subMarkup = costEtbPer1M ? subRate / costEtbPer1M : 0;
  const odMarkup = costEtbPer1M ? odRate / costEtbPer1M : 0;
  const costPerAvgRunEtb = (avgTokensPerRun / 1_000_000) * costEtbPer1M;

  return (
    <section className="panel">
      <div style={{ display: "flex", alignItems: "baseline", justifyContent: "space-between" }}>
        <h2 style={{ marginTop: 0 }}>Pricing model</h2>
        <span style={{ fontSize: 12, color: "#8a8a8a" }}>
          blended cost {usd(blendedUsdPer1M, 4)} / 1M tokens
        </span>
      </div>
      <div
        style={{
          display: "grid",
          gap: 16,
          gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))",
        }}
      >
        <div style={{ display: "grid", gap: 12 }}>
          <Lever label="FX rate (ETB per USD)" value={fx} onChange={setFx} step={1} />
          <Lever label="Subscription rate y (ETB / 1M)" value={subRate} onChange={setSubRate} step={50} />
          <Lever label="On-demand rate x (ETB / 1M)" value={odRate} onChange={setOdRate} step={50} />
        </div>
        <Card
          title="Your cost"
          primary={etb(costEtbPer1M)}
          primarySub="per 1M tokens"
          rows={[
            ["USD / 1M tokens", usd(blendedUsdPer1M, 4)],
            ["ETB / 1M tokens", etb(costEtbPer1M)],
            ["Cost / avg run", etb(costPerAvgRunEtb)],
          ]}
        />
        <Card
          title="Margins at planned rates"
          primary={`${pct(subMarginPct)} / ${pct(odMarginPct)}`}
          primarySub="subscription / on-demand gross margin"
          warn={subMarginPct < 0.5}
          rows={[
            ["Subscription markup", `${subMarkup.toFixed(1)}×`],
            ["On-demand markup", `${odMarkup.toFixed(1)}×`],
            ["Break-even rate", `${etb(costEtbPer1M)} / 1M`],
          ]}
        />
      </div>
    </section>
  );
}
