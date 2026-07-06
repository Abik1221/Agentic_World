"use client";

import * as React from "react";
import { createPortal } from "react-dom";
import { cx } from "./ui";

// Modal is the app's popup primitive: a centered glass card over a blurred
// backdrop. Closes on Escape or backdrop click, locks body scroll, and restores
// focus. Renders through a portal so it escapes any transformed/overflow parent.
export function Modal({
  open,
  onClose,
  title,
  eyebrow,
  children,
  footer,
  size = "md",
}: {
  open: boolean;
  onClose: () => void;
  title?: React.ReactNode;
  eyebrow?: React.ReactNode;
  children: React.ReactNode;
  footer?: React.ReactNode;
  size?: "sm" | "md" | "lg";
}) {
  const [mounted, setMounted] = React.useState(false);
  React.useEffect(() => setMounted(true), []);

  React.useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", onKey);
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = prev;
    };
  }, [open, onClose]);

  if (!mounted || !open) return null;

  const width = { sm: "max-w-sm", md: "max-w-lg", lg: "max-w-2xl" }[size];

  return createPortal(
    <div
      className="fixed inset-0 z-[100] flex items-center justify-center p-4"
      role="dialog"
      aria-modal="true"
    >
      <div
        className="absolute inset-0 bg-bg-deep/70 backdrop-blur-sm animate-[fadeIn_.15s_ease]"
        onClick={onClose}
      />
      <div
        className={cx(
          "glass relative w-full overflow-hidden rounded-xl shadow-glow-teal-lg",
          "animate-[modalIn_.2s_cubic-bezier(.2,.7,.2,1)]",
          width,
        )}
      >
        <div className="flex items-start justify-between gap-4 border-b border-border-soft px-6 py-4">
          <div className="min-w-0">
            {eyebrow && <p className="label-caps mb-1 text-primary">{eyebrow}</p>}
            {title && (
              <h2 className="font-display text-lg font-semibold text-ink-primary">{title}</h2>
            )}
          </div>
          <button
            onClick={onClose}
            aria-label="Close"
            className="-mr-1 rounded-sm p-1 text-ink-faint transition hover:bg-surface-high/60 hover:text-ink-primary"
          >
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <path d="M6 6l12 12M18 6L6 18" />
            </svg>
          </button>
        </div>

        <div className="px-6 py-5 text-sm leading-6 text-ink-dim">{children}</div>

        {footer && (
          <div className="flex items-center justify-end gap-2 border-t border-border-soft bg-surface-high/30 px-6 py-4">
            {footer}
          </div>
        )}
      </div>
    </div>,
    document.body,
  );
}
