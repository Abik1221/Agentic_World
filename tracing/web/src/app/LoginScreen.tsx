"use client";

import { useState } from "react";

export default function LoginScreen() {
  const [user, setUser] = useState("founders@pyyol.com");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const res = await fetch("/api/auth/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ user, password }),
      });
      if (res.ok) {
        window.location.reload();
        return;
      }
      const data = (await res.json().catch(() => ({}))) as { error?: string };
      setError(data.error === "invalid_credentials" ? "Invalid email or password." : "Sign-in failed.");
    } catch {
      setError("Could not reach the server.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login-wrap">
      <form className="login-card" onSubmit={submit}>
        <div className="login-brand">
          <div className="brand-mark">PE</div>
          <div>
            <p className="brand-title">Pyyol Eye</p>
            <p className="brand-subtitle">Arena telemetry</p>
          </div>
        </div>
        <h1 className="login-title">Sign in</h1>
        <label className="login-label" htmlFor="user">Email</label>
        <input
          id="user"
          className="login-input"
          type="email"
          value={user}
          onChange={(e) => setUser(e.target.value)}
          autoComplete="username"
          autoFocus
        />
        <label className="login-label" htmlFor="password">Password</label>
        <input
          id="password"
          className="login-input"
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="current-password"
        />
        {error ? <p className="login-error">{error}</p> : null}
        <button className="login-btn" type="submit" disabled={busy}>
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
