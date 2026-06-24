// Top-level app: providers + route table. Routes under the shell require a
// session; unauthenticated users are bounced to /login.

import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import type { ReactNode } from "react";
import { SessionProvider, useSession } from "@/auth/SessionContext";
import { Toaster } from "@/components/ui/sonner";
import { Layout } from "@/components/Layout";
import { Spinner } from "@/components/ui";
import { Login } from "@/views/Login";
import { Dashboard } from "@/views/Dashboard";
import { Query } from "@/views/Query";
import { RolesDatabases } from "@/views/RolesDatabases";
import { Backups } from "@/views/Backups";
import { Alerts } from "@/views/Alerts";
import { Migrate } from "@/views/Migrate";
import { Settings } from "@/views/Settings";

export default function App() {
  return (
    <SessionProvider>
      <Routes>
        <Route path="/login" element={<LoginGate />} />
        <Route
          element={
            <RequireSession>
              <Layout />
            </RequireSession>
          }
        >
          <Route index element={<Dashboard />} />
          <Route path="query" element={<Query />} />
          <Route path="roles" element={<RolesDatabases />} />
          <Route path="backups" element={<Backups />} />
          <Route path="alerts" element={<Alerts />} />
          <Route path="migrate" element={<Migrate />} />
          <Route path="settings" element={<Settings />} />
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
      <Toaster />
    </SessionProvider>
  );
}

function RequireSession({ children }: { children: ReactNode }) {
  const { ready, authenticated } = useSession();
  const location = useLocation();

  if (!ready) {
    return (
      <div className="boot-screen">
        <Spinner label="Starting…" />
      </div>
    );
  }
  if (!authenticated) {
    return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  }
  return <>{children}</>;
}

/** Redirects already-authenticated users away from the login screen. */
function LoginGate() {
  const { ready, authenticated } = useSession();
  if (ready && authenticated) return <Navigate to="/" replace />;
  return <Login />;
}
