import { useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { Bell, ChevronDown, LogOut, Search, SquareTerminal } from "lucide-react";
import { api, type User } from "@/lib/api";
import { stopStream, useStreamState } from "@/lib/stream";
import { IconButton } from "@/ui/controls";
import { cn } from "@/ui/cn";

export function Header({ user, onToggleDrawer, drawerOpen }: { user: User; onToggleDrawer: () => void; drawerOpen: boolean }) {
  return (
    <header className="bg-surface border-line flex h-10 shrink-0 items-center gap-2 border-b px-2 md:px-3">
      <div className="flex w-[11.5rem] shrink-0 items-center gap-2">
        <img src="/favicon.svg" alt="" className="size-5" />
        <span className="font-semibold tracking-tight">SynCloud</span>
      </div>

      {/* Project / environment switcher: projects arrive with services (Phase 2). */}
      <button
        disabled
        title="Projects arrive in Phase 2"
        className="border-line text-muted hidden h-7 items-center gap-1.5 rounded-sm border px-2 text-xs md:flex"
      >
        <span>default</span>
        <span className="text-faint">/</span>
        <span>production</span>
        <ChevronDown className="size-3" />
      </button>

      <div className="flex flex-1 justify-center">
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

      <StreamIndicator />
      <IconButton label="Notifications" disabled>
        <Bell className="size-4" />
      </IconButton>
      <IconButton label="Toggle Cloud Shell" onClick={onToggleDrawer} className={cn(drawerOpen && "text-fg bg-hover")}>
        <SquareTerminal className="size-4" />
      </IconButton>
      <UserMenu user={user} />
    </header>
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
        <span className="bg-accent/20 text-accent flex size-5 items-center justify-center rounded-sm text-[10px] font-semibold">
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
