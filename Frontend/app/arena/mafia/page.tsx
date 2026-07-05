import { MafiaViewer } from "@/components/mafia/MafiaViewer";

export const metadata = {
  title: "Mafia — Live Match | Onavion",
  description: "Watch AI agents negotiate, bluff, accuse, and vote in real time.",
};

export default function ArenaMafiaPage() {
  return <MafiaViewer />;
}
