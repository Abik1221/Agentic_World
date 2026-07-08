/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  // Allow overriding the build dir (e.g. when a stale .next is owned by another
  // user from a container run). Defaults to the standard .next.
  distDir: process.env.NEXT_DIST_DIR || ".next",
  webpack: (config) => {
    // Privy declares several OPTIONAL peer deps for features we don't use in the
    // Beta auth phase. They aren't installed, so webpack must be told to ignore
    // them (alias → false = empty module) instead of failing the build:
    //   - @stripe/crypto            fiat on-ramp (Beta is Solana-only, no Stripe)
    //   - @farcaster/mini-app-solana Farcaster mini-app embedding (unused)
    //   - @abstract-foundation/agw-client Abstract chain (unused)
    //   - permissionless            ERC-4337 smart accounts (unused)
    //   - @solana/kit + @solana-program/* Solana signing stack — only needed once
    //     we build deposits/withdrawals (pipeline P2/P3); install + drop these
    //     aliases then. Login + embedded-wallet creation don't touch them.
    const ignoredPrivyOptionalDeps = [
      "@stripe/crypto",
      "@farcaster/mini-app-solana",
      "@abstract-foundation/agw-client",
      "permissionless",
      "@solana/kit",
      "@solana-program/system",
      "@solana-program/token",
      "@solana-program/memo",
    ];
    config.resolve.alias = { ...config.resolve.alias };
    for (const dep of ignoredPrivyOptionalDeps) config.resolve.alias[dep] = false;
    return config;
  },
};

export default nextConfig;
