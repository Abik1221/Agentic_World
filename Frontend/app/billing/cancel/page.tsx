import { StatusScreen } from "@/components/auth/ui";

export default function BillingCancelPage() {
  return (
    <StatusScreen
      tone="pending"
      eyebrow="Checkout"
      title="Payment cancelled"
      message="No charge was made. You can return to the wallet anytime to purchase coin packs."
      cta={{ label: "Back to wallet", href: "/wallet" }}
    />
  );
}
