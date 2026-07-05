import { StatusScreen } from "@/components/auth/ui";

export default function SubscriptionCancelPage() {
  return (
    <StatusScreen
      tone="pending"
      eyebrow="Arena Pass"
      title="Checkout cancelled"
      message="No subscription was created. You can try again anytime."
      cta={{ label: "Back to Arena Pass", href: "/subscription" }}
    />
  );
}
