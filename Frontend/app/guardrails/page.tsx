import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { fetchLimits, fetchWallet } from "@/lib/api";
import { serverSession } from "@/lib/session.server";
import { GuardrailsClient } from "./GuardrailsClient";

export default async function GuardrailsPage() {
  const session = serverSession();
  const [wallet, limits] = await Promise.all([fetchWallet(session), fetchLimits(session)]);
  return <GuardrailsClient wallet={wallet} limits={limits} />;
}
