// Login view: a single admin password (set at first run). Surfaces lockout
// clearly since auth.Authenticate returns CodeLocked after repeated failures.

import { useState, type FormEvent } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { ApiError } from "@/api/client";
import { useSession } from "@/auth/SessionContext";

export function Login() {
  const { login } = useSession();
  const navigate = useNavigate();
  const location = useLocation();
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [locked, setLocked] = useState(false);

  const from = (location.state as { from?: string } | null)?.from ?? "/";

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy || !password) return;
    setBusy(true);
    setError(null);
    setLocked(false);
    try {
      await login(password);
      navigate(from, { replace: true });
    } catch (err) {
      if (err instanceof ApiError) {
        setLocked(err.isLocked);
        setError(
          err.isLocked
            ? err.message || "Too many attempts. Try again later."
            : err.code === "auth"
              ? "That password is not correct."
              : err.message,
        );
      } else {
        setError("Could not sign in. Check your connection.");
      }
      setPassword("");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-screen">
      <form className="login-card" onSubmit={onSubmit}>
        <div className="login-brand">
          <span className="brand-mark">pg</span>
          <h1>indiepg</h1>
        </div>
        <p className="login-sub">Sign in with your admin password.</p>

        {error ? (
          <div className={`callout ${locked ? "callout-warn" : "callout-danger"}`} role="alert">
            {error}
          </div>
        ) : null}

        <label className="field">
          <span className="field-label">Admin password</span>
          <input
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            disabled={busy || locked}
            autoFocus
          />
        </label>

        <button type="submit" className="btn btn-primary btn-block" disabled={busy || !password}>
          {busy ? "Signing in…" : "Sign in"}
        </button>

        <p className="login-help muted">
          Forgot it? On the server, run <code>indiepg reset-password</code> over SSH.
        </p>
      </form>
    </div>
  );
}
