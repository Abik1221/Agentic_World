import EventsFeed from "./EventsFeed";
import { fetchQuery, type EventRow } from "@/lib/pyyol-lens-api";

export default async function EventsPage({
  searchParams,
}: {
  searchParams: Promise<{ q?: string }>;
}) {
  const { q } = await searchParams;
  // Empty query returns the full recent stream (the search matches everything),
  // so the page lands on all recent events rather than only errors.
  const query = q?.trim() || "";
  const rows = (await fetchQuery<EventRow[]>(`/v1/search/events?q=${encodeURIComponent(query)}`)) ?? [];

  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Events</h1>
          <p>Free-form search across the canonical event stream.</p>
        </div>
      </section>
      <EventsFeed initialRows={rows} initialQuery={query} />
    </div>
  );
}
