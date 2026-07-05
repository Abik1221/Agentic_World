import { StatusScreen } from "@/components/auth/ui";

export default function PayoutsRefreshPage() {
  return (
    <StatusScreen
      tone="pending"
      eyebrow="Stripe Connect"
      title="Continue setup"
      message="Your onboarding link expired. Start Stripe Connect again from Withdrawals to finish KYC."
      cta={{ label: "Resume onboarding", href: "/withdrawals" }}
    />
  );
}
