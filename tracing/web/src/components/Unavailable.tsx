/** Honest "the bus is down" — never a table of zeros that looks like idle production. */

export function Unavailable({
  title = "Telemetry API unreachable",
  detail,
}: {
  title?: string;
  detail?: string;
}) {
  return (
    <div className="warning-state" role="status">
      <strong>{title}.</strong>{" "}
      {detail ??
        "This is not an empty dataset — Pyyol Eye could not reach the query API, so no numbers are shown."}
    </div>
  );
}

export function NotShipped({ feature }: { feature: string }) {
  return (
    <div className="warning-state" role="status">
      <strong>{feature} is not in this release.</strong> The control API does not store or evaluate
      this yet. The page is here so a bookmark is not a silent empty table.
    </div>
  );
}
