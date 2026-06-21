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
import type { SessionInfo } from "@/api/types";

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
  const [info, setInfo] = useState<SessionInfo>({ authenticated: false });

  const refresh = useCallback(async () => {
    try {
      const session = await api.session();
      setInfo(session);
    } catch {
      setInfo({ authenticated: false });
    } finally {
      setReady(true);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const login = useCallback(async (password: string) => {
    const session = await api.login(password);
    setInfo(session.authenticated ? session : { authenticated: true });
  }, []);

  const logout = useCallback(async () => {
    try {
      await api.logout();
    } finally {
      setInfo({ authenticated: false });
    }
  }, []);

  const value = useMemo<SessionState>(
    () => ({
      ready,
      authenticated: info.authenticated,
      subject: info.subject,
      login,
      logout,
      refresh,
    }),
    [ready, info, login, logout, refresh],
  );

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionState {
  const ctx = useContext(SessionContext);
  if (!ctx) throw new Error("useSession must be used within <SessionProvider>");
  return ctx;
}
