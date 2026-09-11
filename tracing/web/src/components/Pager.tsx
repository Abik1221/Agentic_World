import Link from "next/link";

export function Pager({
  base,
  params,
  total,
  limit,
  offset,
}: {
  base: string;
  params: Record<string, string | undefined>;
  total: number;
  limit: number;
  offset: number;
}) {
  const page = Math.floor(offset / limit) + 1;
  const pages = Math.max(1, Math.ceil(total / limit));
  const qs = (nextOffset: number) => {
    const u = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
      if (v) u.set(k, v);
    }
    u.set("limit", String(limit));
    u.set("offset", String(nextOffset));
    return `${base}?${u.toString()}`;
  };
  const prev = offset > 0 ? Math.max(0, offset - limit) : null;
  const next = offset + limit < total ? offset + limit : null;
  return (
    <nav className="pager" aria-label="Pagination">
      <span className="muted">
        {total.toLocaleString()} total · page {page} of {pages}
      </span>
      <span className="pager-links">
        {prev != null ? <Link href={qs(prev)}>← Previous</Link> : <span className="muted">← Previous</span>}
        {next != null ? <Link href={qs(next)}>Next →</Link> : <span className="muted">Next →</span>}
      </span>
    </nav>
  );
}
