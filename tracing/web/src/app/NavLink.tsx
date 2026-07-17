"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

type NavLinkProps = {
  href: string;
  label: string;
  onNavigate?: () => void;
};

export default function NavLink({ href, label, onNavigate }: NavLinkProps) {
  const pathname = usePathname();
  const isActive = pathname === href || (href !== "/overview" && pathname.startsWith(`${href}/`));

  return (
    <Link
      href={href}
      className={`nav-link${isActive ? " nav-link-active" : ""}`}
      onClick={onNavigate}
      aria-current={isActive ? "page" : undefined}
    >
      {label}
    </Link>
  );
}
