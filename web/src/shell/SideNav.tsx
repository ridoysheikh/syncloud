import { useState } from "react";
import { Link, useRouterState } from "@tanstack/react-router";
import { ChevronDown, ChevronsLeft, ChevronsRight } from "lucide-react";
import { modules } from "@/modules";
import type { DashboardModule } from "@/modules/types";
import { readPref, writePref } from "@/lib/storage";
import { cn } from "@/ui/cn";

function isActive(path: string, current: string) {
  return path === "/" ? current === "/" : current === path || current.startsWith(path + "/");
}

/** Modular side nav built from the module registry (§10.1). Collapses to an icon rail. */
export function SideNav({ collapsed, onToggle }: { collapsed: boolean; onToggle: () => void }) {
  const current = useRouterState({ select: (s) => s.location.pathname });
  const [closed, setClosed] = useState<Record<string, boolean>>(() => readPref("nav.closedGroups", {}));

  const toggleGroup = (id: string) => {
    const next = { ...closed, [id]: !closed[id] };
    setClosed(next);
    writePref("nav.closedGroups", next);
  };

  return (
    <nav
      className={cn(
        "bg-surface border-line flex shrink-0 flex-col border-r transition-[width] duration-150",
        collapsed ? "w-12" : "w-56",
      )}
      aria-label="Main"
    >
      <div className="flex-1 overflow-y-auto py-1.5">
        {modules.map((m) =>
          collapsed ? (
            <RailItem key={m.id} module={m} current={current} />
          ) : (
            <Group key={m.id} module={m} current={current} open={!closed[m.id]} onToggle={() => toggleGroup(m.id)} />
          ),
        )}
      </div>
      <button
        onClick={onToggle}
        className="border-line text-muted hover:text-fg hover:bg-hover flex h-8 items-center gap-2 border-t px-3.5 text-xs"
        aria-label={collapsed ? "Expand navigation" : "Collapse navigation"}
      >
        {collapsed ? <ChevronsRight className="size-4" /> : <ChevronsLeft className="size-4" />}
        {!collapsed && "Collapse"}
      </button>
    </nav>
  );
}

function Group({
  module: m,
  current,
  open,
  onToggle,
}: {
  module: DashboardModule;
  current: string;
  open: boolean;
  onToggle: () => void;
}) {
  const pages = m.pages.filter((p) => !p.hidden);
  const Icon = m.icon;
  const groupActive = pages.some((p) => isActive(p.path, current));
  // Only the most specific match is highlighted (/registry vs /registry/repos).
  const best = pages
    .filter((p) => isActive(p.path, current))
    .sort((a, b) => b.path.length - a.path.length)[0];

  // A module with a single page is a plain link.
  if (pages.length === 1) {
    const p = pages[0]!;
    return (
      <Link to={p.path} className={itemClass(isActive(p.path, current))}>
        <Icon className="size-4 shrink-0" strokeWidth={1.75} />
        <span className="truncate">{m.label}</span>
      </Link>
    );
  }
  return (
    <div>
      <button onClick={onToggle} className={cn(itemClass(false), groupActive && "text-fg")} aria-expanded={open}>
        <Icon className="size-4 shrink-0" strokeWidth={1.75} />
        <span className="flex-1 truncate text-left">{m.label}</span>
        <ChevronDown className={cn("size-3.5 transition-transform", !open && "-rotate-90")} />
      </button>
      {open &&
        pages.map((p) => (
          <Link key={p.path} to={p.path} className={cn(itemClass(p === best), "pl-9.5")}>
            <span className="truncate">{p.label}</span>
          </Link>
        ))}
    </div>
  );
}

function RailItem({ module: m, current }: { module: DashboardModule; current: string }) {
  const first = m.pages.find((p) => !p.hidden);
  if (!first) return null;
  const Icon = m.icon;
  const active = m.pages.some((p) => isActive(p.path, current));
  return (
    <Link to={first.path} title={m.label} aria-label={m.label} className={cn(itemClass(active), "justify-center px-0")}>
      <Icon className="size-4" strokeWidth={1.75} />
    </Link>
  );
}

function itemClass(active: boolean) {
  return cn(
    "mx-1.5 flex h-7 items-center gap-2 rounded-sm px-2 text-[13px] transition-colors",
    active ? "bg-raised text-fg shadow-[inset_2px_0_0_var(--color-accent)]" : "text-muted hover:text-fg hover:bg-hover",
  );
}
