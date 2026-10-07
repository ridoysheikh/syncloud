import { useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, ChevronDown, LogOut, Menu, Search, SquareTerminal, TriangleAlert, X } from "lucide-react";
import { backupQuery } from "@/lib/backups";
import { api, type User } from "@/lib/api";
import { stopStream, useStreamState } from "@/lib/stream";
import { IconButton } from "@/ui/controls";
import { cn } from "@/ui/cn";
import { useActiveAlerts } from "@/modules/monitoring/AlertsPage";

export function Header({
  user,
  onToggleDrawer,
  drawerOpen,
  onToggleMenu,
  menuOpen,
}: {
  user: User;
  onToggleDrawer: () => void;
  drawerOpen: boolean;
  /** Set on phone-sized screens, where the nav is a menu. */
  onToggleMenu?: () => void;
  menuOpen?: boolean;
}) {
  return (
    <header className="bg-surface border-line flex h-10 shrink-0 items-center gap-1 border-b px-2 sm:gap-2 md:px-3">
      {onToggleMenu && (
        <IconButton label={menuOpen ? "Close menu" : "Open menu"} onClick={onToggleMenu} aria-expanded={menuOpen}>
          {menuOpen ? <X className="size-4" /> : <Menu className="size-4" />}
        </IconButton>
      )}
      <div className="flex shrink-0 items-center gap-2 lg:w-[11.5rem]">
        <img src="/favicon.svg" alt="" className="size-5" />
        <span className="font-semibold tracking-tight">SynCloud</span>
      </div>

      {/* Project / environment switcher: projects arrive with services (Phase 2). */}
      <button
        disabled
        title="Projects arrive in Phase 2"
        className="border-line text-muted hidden h-7 items-center gap-1.5 rounded-sm border px-2 text-xs lg:flex"
      >
        <span>default</span>
        <span className="text-faint">/</span>
        <span>production</span>
        <ChevronDown className="size-3" />
      </button>

      <div className="flex min-w-0 flex-1 justify-center">
        <button
          disabled
          title="Global search and the ⌘K command palette arrive with the first resources"
          className="bg-bg border-line text-faint hidden h-7 w-full max-w-md items-center gap-2 rounded-sm border px-2 text-xs sm:flex"
        >
          <Search className="size-3.5" />
          <span className="flex-1 text-left">Search resources…</span>
          <kbd className="border-line rounded-sm border px-1 font-mono text-[10px]">⌘K</kbd>
        </button>
      </div>

      <BackupWarning />
      <StreamIndicator />
      <AlertBell />
      <IconButton label="Toggle Cloud Shell" onClick={onToggleDrawer} className={cn(drawerOpen && "text-fg bg-hover")}>
        <SquareTerminal className="size-4" />
      </IconButton>
      <UserMenu user={user} />
    </header>
  );
}

/** Firing alerts (§9): a count on the bell, which opens the Alerts page. */
function AlertBell() {
  const { data = [] } = useActiveAlerts();
  const firing = data.filter((a) => a.state === "firing");
  const critical = firing.some((a) => a.severity === "critical");
  return (
    <Link
      to={"/monitoring/alerts" as string}
      title={firing.length ? `${firing.length} alert${firing.length > 1 ? "s" : ""} firing` : "No alerts firing"}
      className="text-muted hover:text-fg hover:bg-hover relative flex size-7 items-center justify-center rounded-sm"
    >
      <Bell className="size-4" />
      {firing.length > 0 && (
        <span
          className={cn(
            "absolute -top-0.5 -right-0.5 min-w-3.5 rounded-full px-1 text-center text-[9px] leading-3.5 font-semibold text-accent-fg",
            critical ? "bg-bad" : "bg-warn",
          )}
        >
          {firing.length}
        </span>
      )}
    </Link>
  );
}

/** A permanent warning until S3 backups are configured and working (§5.0). */
function BackupWarning() {
  const { data } = useQuery(backupQuery);
  if (!data) return null;
  const failing = data.configured && data.status.lastError !== "";
  if (data.configured && !failing) return null;
  const backupsPath: string = "/settings/backups";
  return (
    <Link
      to={backupsPath}
      className="border-warn/20 bg-warn/10 text-warn hidden h-7 items-center gap-1.5 rounded-sm border px-2 text-xs font-medium md:flex"
      title={failing ? data.status.lastError : "The controller is not backed up. Configure an S3 destination."}
      aria-label={failing ? "Backup failing" : "Backups are off"}
    >
      <TriangleAlert className="size-3.5" />
      <span className="hidden lg:inline">{failing ? "Backup failing" : "Backups are off"}</span>
    </Link>
  );
}

function StreamIndicator() {
  const s = useStreamState();
  const label = s === "open" ? "Live" : s === "connecting" ? "Connecting…" : "Offline";
  return (
    <span className="text-muted hidden items-center gap-1.5 px-1 text-xs sm:flex" title={`Live updates: ${label}`}>
      <span
        className={cn(
          "size-1.5 rounded-full",
          s === "open" ? "bg-ok" : s === "connecting" ? "bg-warn animate-pulse" : "bg-bad",
        )}
      />
      {label}
    </span>
  );
}

function UserMenu({ user }: { user: User }) {
  const [open, setOpen] = useState(false);
  const qc = useQueryClient();
  const navigate = useNavigate();

  const logout = async () => {
    await api("POST", "/auth/logout").catch(() => undefined);
    stopStream();
    qc.setQueryData(["auth", "me"], null);
    await navigate({ to: "/login" });
  };

  return (
    <div className="relative">
      <button
        onClick={() => setOpen((o) => !o)}
        onBlur={(e) => !e.currentTarget.parentElement?.contains(e.relatedTarget) && setOpen(false)}
        className="hover:bg-hover flex h-7 items-center gap-1.5 rounded-sm px-1.5 text-xs"
        aria-haspopup="menu"
        aria-expanded={open}
      >
        <span className="bg-accent/10 text-accent flex size-5 items-center justify-center rounded-sm text-[10px] font-semibold">
          {user.name.slice(0, 1).toUpperCase()}
        </span>
        <span className="hidden max-w-32 truncate md:inline">{user.name}</span>
        <ChevronDown className="text-muted size-3" />
      </button>
      {open && (
        <div role="menu" className="bg-raised border-line-strong absolute right-0 z-20 mt-1 w-56 rounded-md border py-1 shadow-lg">
          <div className="border-line border-b px-2.5 pb-1.5">
            <div className="truncate text-xs font-medium">{user.name}</div>
            <div className="text-muted truncate text-xs">{user.email}</div>
            {user.isRoot && <div className="text-warn mt-0.5 text-[10px] uppercase">Root account</div>}
          </div>
          <button role="menuitem" onClick={logout} className="hover:bg-hover flex w-full items-center gap-2 px-2.5 py-1.5 text-xs">
            <LogOut className="size-3.5" />
            Sign out
          </button>
        </div>
      )}
    </div>
  );
}
