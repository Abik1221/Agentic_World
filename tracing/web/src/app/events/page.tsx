import EventsFeed from "./EventsFeed";
import { Unavailable } from "@/components/Unavailable";
import { fetchQueryResult, type EventRow } from "@/lib/pyyol-lens-api";

export default async function EventsPage({
  searchParams,
}: {
  searchParams: Promise<{ q?: string }>;
}) {
  const { q } = await searchParams;
  // Empty query returns the full recent stream (the search matches everything),
  // so the page lands on all recent events rather than only errors.
  const query = q?.trim() || "";
  const result = await fetchQueryResult<EventRow[]>(`/v1/search/events?q=${encodeURIComponent(query)}`);

  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Events</h1>
          <p>Free-form search across the canonical event stream.</p>
        </div>
      </section>
      {result.ok ? (
        <EventsFeed initialRows={result.data} initialQuery={query} />
      ) : (
        <Unavailable title="Event stream unavailable" />
      )}
    </div>
  );
}
