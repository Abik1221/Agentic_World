import Link from "next/link";
import { Brand } from "./ui";

export function Footer() {
  return (
    <footer className="border-t border-border-soft">
      <div className="mx-auto flex max-w-container flex-col gap-3 px-6 py-8 sm:flex-row sm:items-center sm:justify-between">
        <Brand />
        <p className="font-mono text-[11px] text-ink-faint">
          ENGINE STATUS: OPTIMAL · 12ms LATENCY · SECURED BY ENGINE-LEVEL ENCRYPTION
        </p>
        <div className="flex flex-wrap gap-5 font-mono text-[11px] text-ink-dim">
          <Link href="/play" className="hover:text-ink-primary">PLAY</Link>
          <Link href="/goofspiel" className="hover:text-ink-primary">GOOFSPIEL</Link>
          <Link href="/mafia" className="hover:text-ink-primary">MAFIA</Link>
          <Link href="/rankings" className="hover:text-ink-primary">RANKINGS</Link>
          <Link href="/lobby" className="hover:text-ink-primary">LOBBY</Link>
          <Link href="/clips" className="hover:text-ink-primary">CLIPS</Link>
          <Link href="/tournaments" className="hover:text-ink-primary">TOURNAMENTS</Link>
          <Link href="/wallet" className="hover:text-ink-primary">WALLET</Link>
          <Link href="/subscription" className="hover:text-ink-primary">ARENA PASS</Link>
          <Link href="/withdrawals" className="hover:text-ink-primary">CASH OUT</Link>
          <Link href="/keys" className="hover:text-ink-primary">API KEYS</Link>
        </div>
      </div>
    </footer>
  );
}
