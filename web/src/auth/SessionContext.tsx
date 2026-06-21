// Session context: holds the current authenticated state and exposes login /
// logout. The actual credential is an HttpOnly cookie set by the server, so we
// only track a boolean + subject here.

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { api } from "@/api/client";

interface SessionState {
  ready: boolean;
  authenticated: boolean;
  subject?: string;
  login: (password: string) => Promise<void>;
  logout: () => Promise<void>;
  refresh: () => Promise<void>;
}

const SessionContext = createContext<SessionState | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [ready, setReady] = useState(false);
  const [authenticated, setAuthenticated] = useState(false);
  const [subject, setSubject] = useState<string | undefined>(undefined);

  // Best-effort: load the signed-in admin's subject for the header. A failure
  // here must never block the authenticated state, so it falls back to unset.
  const loadSubject = useCallback(async () => {
    try {
      const who = await api.whoami();
      setSubject(who.subject);
    } catch {
      setSubject(undefined);
    }
  }, []);

  const refresh = useCallback(async () => {
    try {
      const status = await api.session();
      setAuthenticated(status.authenticated);
      if (status.authenticated) {
        await loadSubject();
      } else {
        setSubject(undefined);
      }
    } catch {
      setAuthenticated(false);
      setSubject(undefined);
    } finally {
      setReady(true);
    }
  }, [loadSubject]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const login = useCallback(
    async (password: string) => {
      // api.login throws on failure; success sets the HttpOnly session cookie.
      await api.login(password);
      setAuthenticated(true);
      await loadSubject();
    },
    [loadSubject],
  );

  const logout = useCallback(async () => {
    try {
      await api.logout();
    } finally {
      setAuthenticated(false);
      setSubject(undefined);
    }
  }, []);

  const value = useMemo<SessionState>(
    () => ({
      ready,
      authenticated,
      subject,
      login,
      logout,
      refresh,
    }),
    [ready, authenticated, subject, login, logout, refresh],
  );

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionState {
  const ctx = useContext(SessionContext);
  if (!ctx) throw new Error("useSession must be used within <SessionProvider>");
  return ctx;
}
