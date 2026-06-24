// Application shell: shadcn Sidebar navigation + top bar + main content outlet.

import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import {
  Archive,
  ArrowLeftRight,
  Bell,
  Database,
  LayoutDashboard,
  LogOut,
  Settings,
  SquareTerminal,
  type LucideIcon,
} from "lucide-react";
import { useSession } from "@/auth/SessionContext";
import { useToast } from "./Toast";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import { Separator } from "@/components/ui/separator";

interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  /** Exact-match the route (only the index/Dashboard route). */
  end?: boolean;
}

const NAV: NavItem[] = [
  { to: "/", label: "Dashboard", icon: LayoutDashboard, end: true },
  { to: "/query", label: "Query", icon: SquareTerminal },
  { to: "/roles", label: "Roles & Databases", icon: Database },
  { to: "/backups", label: "Backups", icon: Archive },
  { to: "/alerts", label: "Alerts", icon: Bell },
  { to: "/migrate", label: "Migrate", icon: ArrowLeftRight },
  { to: "/settings", label: "Settings", icon: Settings },
];

export function Layout() {
  const { logout, subject } = useSession();
  const toast = useToast();
  const navigate = useNavigate();
  const location = useLocation();

  const onLogout = async () => {
    await logout();
    toast.info("Signed out.");
    navigate("/login", { replace: true });
  };

  const isActive = (item: NavItem) =>
    item.end ? location.pathname === item.to : location.pathname.startsWith(item.to);

  const currentLabel = NAV.find(isActive)?.label ?? "";

  return (
    <SidebarProvider>
      <Sidebar>
        <SidebarHeader>
          <div className="flex items-center gap-2 px-2 py-1">
            <span className="flex size-8 items-center justify-center rounded-md bg-primary text-sm font-bold text-primary-foreground">
              pg
            </span>
            <span className="text-base font-semibold">indiepg</span>
          </div>
        </SidebarHeader>
        <SidebarContent>
          <SidebarGroup>
            <SidebarGroupContent>
              <SidebarMenu>
                {NAV.map((item) => (
                  <SidebarMenuItem key={item.to}>
                    <SidebarMenuButton asChild isActive={isActive(item)}>
                      <NavLink to={item.to} end={item.end}>
                        <item.icon />
                        <span>{item.label}</span>
                      </NavLink>
                    </SidebarMenuButton>
                  </SidebarMenuItem>
                ))}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        </SidebarContent>
        <SidebarFooter>
          <div className="flex flex-col gap-0.5 px-2 py-1 text-xs">
            <span className="text-muted-foreground">Signed in</span>
            <span className="font-medium">{subject ?? "admin"}</span>
          </div>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton onClick={onLogout}>
                <LogOut />
                <span>Sign out</span>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarFooter>
      </Sidebar>
      <SidebarInset>
        <header className="flex h-14 shrink-0 items-center gap-2 border-b px-4">
          <SidebarTrigger />
          <Separator orientation="vertical" className="h-4" />
          <span className="text-sm font-medium">{currentLabel}</span>
        </header>
        <main className="flex-1 overflow-y-auto p-6 lg:p-8">
          <Outlet />
        </main>
      </SidebarInset>
    </SidebarProvider>
  );
}
