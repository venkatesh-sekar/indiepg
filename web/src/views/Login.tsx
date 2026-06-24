// Login view: a single admin password (set at first run). Surfaces lockout
// clearly since auth.Authenticate returns CodeLocked after repeated failures.

import { useState, type FormEvent } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { ApiError } from "@/api/client";
import { useSession } from "@/auth/SessionContext";
import { Callout } from "@/components/ui";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";

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
    <div className="grid min-h-screen place-items-center bg-background p-6">
      <form onSubmit={onSubmit} className="w-full max-w-sm">
        <Card>
          <CardHeader>
            <div className="flex items-center gap-2.5">
              <span className="grid size-8 place-items-center rounded-lg bg-primary font-bold text-primary-foreground">
                pg
              </span>
              <CardTitle className="text-xl">indiepg</CardTitle>
            </div>
            <CardDescription>Sign in with your admin password.</CardDescription>
          </CardHeader>

          <CardContent>
            <FieldGroup>
              {error ? (
                <Callout id="login-error" tone={locked ? "warn" : "danger"}>
                  {error}
                </Callout>
              ) : null}

              <Field data-invalid={error ? "true" : undefined}>
                <FieldLabel htmlFor="password">Admin password</FieldLabel>
                <Input
                  id="password"
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  disabled={busy || locked}
                  aria-invalid={error ? true : undefined}
                  aria-describedby={error ? "login-error" : undefined}
                  autoFocus
                />
              </Field>

              <Button type="submit" className="w-full" disabled={busy || !password}>
                {busy ? (
                  <>
                    <Spinner data-icon="inline-start" />
                    Signing in…
                  </>
                ) : (
                  "Sign in"
                )}
              </Button>
            </FieldGroup>
          </CardContent>

          <CardFooter className="justify-center">
            <p className="text-center text-xs text-muted-foreground">
              Forgot it? On the server, run <code className="font-mono">indiepg reset-password</code>{" "}
              over SSH.
            </p>
          </CardFooter>
        </Card>
      </form>
    </div>
  );
}
