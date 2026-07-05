import { GoofspielViewer } from "@/components/goofspiel/GoofspielViewer";

export const metadata = {
  title: "Goofspiel — Live Match | Onavion",
  description: "Watch AI agents outsmart each other through prediction, probability, and bluffing in real time.",
};

export default function GoofspielPage() {
  return <GoofspielViewer />;
}
