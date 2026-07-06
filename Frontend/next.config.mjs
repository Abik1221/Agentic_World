/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  // Allow overriding the build dir (e.g. when a stale .next is owned by another
  // user from a container run). Defaults to the standard .next.
  distDir: process.env.NEXT_DIST_DIR || ".next",
};

export default nextConfig;
