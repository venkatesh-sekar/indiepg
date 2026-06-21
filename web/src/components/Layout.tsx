// Application shell: sidebar navigation + top bar + main content outlet.

import { NavLink, Outlet, useNavigate } from "react-router-dom";
import { useSession } from "@/auth/SessionContext";
import { useToast } from "./Toast";

interface NavItem {
  to: string;
  label: string;
  icon: string;
  /** Plain-language hint shown under the label. */
  hint: string;
}

const NAV: NavItem[] = [
  { to: "/", label: "Dashboard", icon: "▣", hint: "Health at a glance" },
  { to: "/query", label: "Query", icon: "⌗", hint: "Run read-only SQL" },
  { to: "/roles", label: "Roles & Databases", icon: "◷", hint: "Users and databases" },
  { to: "/backups", label: "Backups", icon: "▤", hint: "Backup & restore" },
  { to: "/alerts", label: "Alerts", icon: "◬", hint: "Notifications & rules" },
  { to: "/migrate", label: "Migrate", icon: "⇄", hint: "Move a database here" },
  { to: "/settings", label: "Settings", icon: "⚙", hint: "Backup storage & retention" },
];

export function Layout() {
  const { logout, subject } = useSession();
  const toast = useToast();
  const navigate = useNavigate();

  const onLogout = async () => {
    await logout();
    toast.info("Signed out.");
    navigate("/login", { replace: true });
  };

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark">pg</span>
          <span className="brand-name">indiepg</span>
        </div>
        <nav className="nav">
          {NAV.map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.to === "/"}
              className={({ isActive }) => `nav-item ${isActive ? "active" : ""}`}
            >
              <span className="nav-icon" aria-hidden="true">
                {item.icon}
              </span>
              <span className="nav-text">
                <span className="nav-label">{item.label}</span>
                <span className="nav-hint">{item.hint}</span>
              </span>
            </NavLink>
          ))}
        </nav>
        <div className="sidebar-foot">
          <div className="sidebar-user">
            <span className="muted">Signed in</span>
            <span>{subject ?? "admin"}</span>
          </div>
          <button type="button" className="btn btn-sm btn-block" onClick={onLogout}>
            Sign out
          </button>
        </div>
      </aside>
      <main className="content">
        <Outlet />
      </main>
    </div>
  );
}
