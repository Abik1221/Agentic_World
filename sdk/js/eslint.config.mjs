// ESLint flat config for the JS SDK.
//
// WHY THIS FILE EXISTS. This package had no lint script and no ESLint config at all — nothing
// linted it, ever. That is the second instance of the same pattern in this repo (Pyyol_client had
// a `lint` script pointing at a command Next 16 removed), and both failed the same quiet way: not
// with an error, but with no signal whatsoever.
//
// It matters more here than in an app. This is a PUBLISHED package: a mistake ships to every
// developer who installs it, and the Python SDK beside it has had ruff configured all along, so
// the two halves of one product were held to different standards.
//
// Deliberately NOT type-aware. Type-aware rules need a full type build per lint run, and `npm test`
// already compiles the whole package with tsc — the same errors twice at double the cost. This
// layer is for what a type checker cannot see.

import js from "@eslint/js";
import tseslint from "typescript-eslint";

export default tseslint.config(
  {
    // Build output and dependencies. `dist` in particular is compiled from `src`, so linting it
    // reports every finding twice and attributes it to a file nobody edits.
    ignores: ["dist/**", "node_modules/**", "coverage/**", "src/version.ts"],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    // Build scripts run in Node, not the browser or a bundler. Without this, `console` and friends
    // read as undefined globals — a false positive that would push someone to import something
    // Node has had built in forever.
    files: ["scripts/**/*.mjs", "*.mjs"],
    languageOptions: {
      globals: { console: "readonly", process: "readonly", URL: "readonly", fetch: "readonly" },
    },
  },
  {
    // Tests build fake WebSocket and client classes, and a mock that must behave like the real
    // object legitimately aliases `this` inside a function-style method. The rule is right about
    // production code and wrong about a stub, so it is relaxed HERE rather than switched off
    // globally — the distinction is the point.
    files: ["src/test/**/*.ts"],
    rules: { "@typescript-eslint/no-this-alias": "off" },
  },
  {
    rules: {
      // The type checker owns unused-variable reporting for TS; two messages for one problem
      // trains people to skim both. Kept as a warning with the conventional underscore escape.
      "@typescript-eslint/no-unused-vars": [
        "warn",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_", caughtErrorsIgnorePattern: "^_" },
      ],
      // `any` is a warning, not an error. This SDK parses provider responses whose shapes are
      // genuinely unknown at the boundary — that is the whole job of instrument.ts — and forcing
      // a cast there would hide the same uncertainty less honestly.
      "@typescript-eslint/no-explicit-any": "warn",
      // An empty catch is how instrumentation stays non-fatal, and this SDK does it deliberately
      // in several places: telemetry must never break a developer's model call. Allowed, because
      // the alternative is a `void 0` in every one of them that says less.
      "no-empty": ["error", { allowEmptyCatch: true }],
    },
  },
);
