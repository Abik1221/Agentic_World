import { StatusScreen } from "@/components/auth/ui";

export default function PayoutsReturnPage() {
  return (
    <StatusScreen
      tone="ok"
      eyebrow="Stripe Connect"
      title="Payout setup complete"
      message="Your identity verification is on file. You can now request withdrawals of net winnings after the clearing window."
      cta={{ label: "Go to cash out", href: "/withdrawals" }}
    />
  );
}
