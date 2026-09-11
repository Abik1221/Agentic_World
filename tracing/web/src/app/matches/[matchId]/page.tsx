import { redirect } from "next/navigation";

export default async function MatchAliasPage({
  params,
  searchParams,
}: {
  params: Promise<{ matchId: string }>;
  searchParams: Promise<{ view?: string }>;
}) {
  const { matchId } = await params;
  const { view } = await searchParams;
  const qs = view ? `?view=${encodeURIComponent(view)}` : "";
  redirect(`/games/${encodeURIComponent(matchId)}${qs}`);
}
