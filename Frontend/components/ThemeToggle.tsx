"use client";

import { useEffect, useState } from "react";

// Light/dark toggle. Dark = the `.broadcast` scope on <body> (graphite esports);
// light = the default cream palette. Preference persists in localStorage and is
// applied before paint by the inline script in app/layout.tsx (no flash).
export function ThemeToggle() {
  const [dark, setDark] = useState(true);

  useEffect(() => {
    setDark(document.body.classList.contains("broadcast"));
  }, []);

  function toggle() {
    const isDark = document.body.classList.toggle("broadcast");
    document.documentElement.style.colorScheme = isDark ? "dark" : "light";
    try {
      localStorage.setItem("aa_theme", isDark ? "dark" : "light");
    } catch {
      /* ignore */
    }
    setDark(isDark);
  }

  return (
    <button
      onClick={toggle}
      title={dark ? "Switch to light mode" : "Switch to dark mode"}
      aria-label="Toggle theme"
      className="grid h-9 w-9 place-items-center rounded-md border border-border-strong text-ink-dim transition hover:text-ink-primary"
    >
      {dark ? "☾" : "☀"}
    </button>
  );
}
