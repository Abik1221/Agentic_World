import { badRateClass, goodRateClass, pct } from "@/lib/benchmark-format";

/** A rate value with a proportional bar. kind="good" (win/legal, higher better)
 *  or kind="bad" (fallback/timeout, lower better) picks the color threshold. */
export function RateCell({ value, kind }: { value: number; kind: "good" | "bad" }) {
  const cls = kind === "good" ? goodRateClass(value) : badRateClass(value);
  const barTone =
    cls === "status-ok" ? "rate-bar-good" : cls === "status-warn" ? "rate-bar-warn" : "rate-bar-bad";
  const width = `${Math.max(0, Math.min(1, value)) * 100}%`;
  return (
    <div className="rate-cell">
      <span className="rate-value">{pct(value)}</span>
      <div className={`rate-bar ${barTone}`}>
        <span style={{ width }} />
      </div>
    </div>
  );
}

/** Win / loss / draw record. */
export function WLD({ w, l, d }: { w: number; l: number; d: number }) {
  return (
    <span className="wld">
      <span className="w">{w}</span>
      <span className="d"> / </span>
      <span className="l">{l}</span>
      {d > 0 ? (
        <>
          <span className="d"> / </span>
          <span className="d">{d}</span>
        </>
      ) : null}
    </span>
  );
}
