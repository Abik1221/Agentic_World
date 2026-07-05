import { redirect } from "next/navigation";

// The Mafia experience now lives in the console viewer at /arena/mafia
// (the old 1850-line spectator hub was replaced). Keep this route as a redirect
// so existing links resolve.
export default function MafiaPage() {
  redirect("/arena/mafia");
}
