import EventsFeed from "./EventsFeed";
import { fetchQuery, type EventRow } from "@/lib/pyyol-lens-api";

export default async function EventsPage({
  searchParams,
}: {
  searchParams: Promise<{ q?: string }>;
}) {
  const { q } = await searchParams;
  const query = q?.trim() || "error";
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
