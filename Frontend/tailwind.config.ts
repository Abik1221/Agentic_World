import type { Config } from "tailwindcss";

const config: Config = {
  content: [
    "./app/**/*.{ts,tsx}",
    "./components/**/*.{ts,tsx}",
    "./lib/**/*.{ts,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        // Token VALUES are CSS variables (channel triplets) so a `.broadcast`
        // scope can flip match views to dark graphite while utility pages stay
        // cream — see :root and .broadcast in app/globals.css.
        surface: "rgb(var(--c-surface) / <alpha-value>)",
        "surface-bright": "rgb(var(--c-surface-bright) / <alpha-value>)",
        "surface-lowest": "rgb(var(--c-surface-lowest) / <alpha-value>)",
        "surface-low": "rgb(var(--c-surface-low) / <alpha-value>)",
        "surface-container": "rgb(var(--c-surface-container) / <alpha-value>)",
        "surface-high": "rgb(var(--c-surface-high) / <alpha-value>)",
        "surface-highest": "rgb(var(--c-surface-highest) / <alpha-value>)",
        "on-surface": "rgb(var(--c-on-surface) / <alpha-value>)",
        "on-surface-variant": "rgb(var(--c-on-surface-variant) / <alpha-value>)",
        outline: "rgb(var(--c-outline) / <alpha-value>)",
        "outline-variant": "rgb(var(--c-outline-variant) / <alpha-value>)",

        primary: "rgb(var(--c-primary) / <alpha-value>)",
        "primary-container": "rgb(var(--c-primary-container) / <alpha-value>)",
        "primary-dim": "rgb(var(--c-primary-dim) / <alpha-value>)",
        "on-primary": "rgb(var(--c-on-primary) / <alpha-value>)",
        "on-primary-container": "rgb(var(--c-on-primary-container) / <alpha-value>)",

        secondary: "rgb(var(--c-secondary) / <alpha-value>)",
        "secondary-dim": "rgb(var(--c-secondary-dim) / <alpha-value>)",
        "on-secondary": "rgb(var(--c-on-secondary) / <alpha-value>)",
        "secondary-container": "rgb(var(--c-secondary-container) / <alpha-value>)",

        tertiary: "rgb(var(--c-tertiary) / <alpha-value>)",
        "tertiary-container": "rgb(var(--c-tertiary-container) / <alpha-value>)",
        "on-tertiary": "rgb(var(--c-on-tertiary) / <alpha-value>)",

        error: "rgb(var(--c-error) / <alpha-value>)",
        "status-error": "rgb(var(--c-status-error) / <alpha-value>)",

        "bg-deep": "rgb(var(--c-bg-deep) / <alpha-value>)",
        "surface-slate": "rgb(var(--c-surface-slate) / <alpha-value>)",
        "panel-navy": "rgb(var(--c-panel-navy) / <alpha-value>)",
        "panel-accent": "rgb(var(--c-panel-accent) / <alpha-value>)",
        "ink-primary": "rgb(var(--c-ink-primary) / <alpha-value>)",
        "ink-dim": "rgb(var(--c-ink-dim) / <alpha-value>)",
        "ink-faint": "rgb(var(--c-ink-faint) / <alpha-value>)",
        "border-strong": "rgb(var(--c-border-strong) / <alpha-value>)",
        "border-soft": "rgb(var(--c-border-soft) / <alpha-value>)",
      },
      fontFamily: {
        display: ["var(--font-grotesk)", "sans-serif"],
        sans: ["var(--font-inter)", "sans-serif"],
        mono: ["var(--font-mono)", "monospace"],
      },
      borderRadius: {
        sm: "0.125rem",
        DEFAULT: "0.25rem",
        md: "0.375rem",
        lg: "0.5rem",
        xl: "0.75rem",
      },
      maxWidth: {
        container: "1200px",
      },
      boxShadow: {
        "glow-teal": "0 0 10px rgba(99,102,241,0.30)",
        "glow-teal-lg": "0 0 26px rgba(99,102,241,0.18)",
        "glow-amber": "0 0 10px rgba(245,158,11,0.30)",
      },
      backgroundImage: {
        "panel-grad": "linear-gradient(180deg, rgba(255,255,255,0.65), rgba(24,27,32,0.03))",
        redact:
          "repeating-linear-gradient(45deg, rgba(24,27,32,0.05) 0, rgba(24,27,32,0.05) 1px, transparent 1px, transparent 7px)",
      },
      letterSpacing: {
        caps: "2px",
      },
    },
  },
  plugins: [],
};

export default config;
