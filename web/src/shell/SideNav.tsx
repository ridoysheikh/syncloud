import { useEffect, useRef, useState } from "react";
import { Link, useRouterState } from "@tanstack/react-router";
import { ChevronDown, ChevronsLeft, ChevronsRight } from "lucide-react";
import { modules } from "@/modules";
import type { DashboardModule } from "@/modules/types";
import { readPref, writePref } from "@/lib/storage";
import { cn } from "@/ui/cn";

// The router's own "active" marking is a URL prefix test that would also set
// aria-current on parents (/projects on /projects/quotas); only exact matches
// may agree with activeNav, which owns aria-current here.
const exact = { exact: true, includeSearch: false } as const;

const segments = (path: string) => path.split("/").filter(Boolean);

/** Whether route pattern `prefix` is a whole-segment prefix of `path` ("/compute/nodes" of "/compute/nodes/$name"). */
function isPrefix(prefix: string, path: string) {
  const a = segments(prefix);
  const b = segments(path);
  return a.length <= b.length && a.every((x, i) => x === b[i]);
}

const visible = modules.flatMap((m) => m.pages.filter((p) => !p.hidden).map((p) => ({ module: m, page: p })));

/**
 * The nav entry for the route the router matched (its pattern, e.g.
 * "/projects/$project/$env/services/$name"): the page itself when it is in
 * the nav, else the nav page that is its longest whole-segment prefix (in its
 * own module first, then anywhere: a project's new-database wizard lives
 * under Projects), else the module's first page. Pattern matching, not URL prefixes, so "/projects/quotas"
 * never also lights "/projects" and a project called "quotas" lights
 * Projects.
 */
export function activeNav(pattern: string): { module: string; path: string } | null {
  const own = modules.find((m) => m.pages.some((p) => p.path === pattern));
  const direct = visible.find((v) => v.page.path === pattern);
  if (direct) return { module: direct.module.id, path: direct.page.path };
  const longest = (xs: typeof visible) =>
    xs.filter((v) => v.page.path !== "/" && isPrefix(v.page.path, pattern)).sort((x, y) => segments(y.page.path).length - segments(x.page.path).length)[0];
  const hit = longest(visible.filter((v) => v.module === own)) ?? longest(visible) ?? visible.find((v) => v.module === own);
  return hit ? { module: hit.module.id, path: hit.page.path } : null;
}

/** Modular side nav built from the module registry (§10.1). Collapses to an icon rail. */
export function SideNav({ collapsed, onToggle }: { collapsed: boolean; onToggle: () => void }) {
  // The matched route's pattern (the deepest match), not the raw URL.
  const pattern = useRouterState({ select: (s) => s.matches[s.matches.length - 1]?.fullPath ?? s.location.pathname });
  const [closed, setClosed] = useState<Record<string, boolean>>(() => readPref("nav.closedGroups", {}));
  const active = activeNav(pattern === "" ? "/" : pattern);
  const navRef = useRef<HTMLDivElement>(null);
  // Bring the active entry into view when the route changes (long navs scroll).
  // Navigating into a closed group opens it; it can be closed again after.
  useEffect(() => {
    if (active && closed[active.module]) {
      const next = { ...closed, [active.module]: false };
      setClosed(next);
      writePref("nav.closedGroups", next);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active?.module, active?.path]);
  useEffect(() => {
    navRef.current?.querySelector<HTMLElement>('[aria-current="page"]')?.scrollIntoView({ block: "nearest" });
  }, [active?.path, collapsed, active && closed[active.module]]);

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
      <div ref={navRef} className="flex flex-1 flex-col gap-px overflow-y-auto px-1.5 py-1.5">
        {modules.map((m) =>
          collapsed ? (
            <RailItem key={m.id} module={m} active={active?.module === m.id} />
          ) : (
            <Group
              key={m.id}
              module={m}
              activePath={active?.module === m.id ? active.path : null}
              open={!closed[m.id]}
              onToggle={() => toggleGroup(m.id)}
            />
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
  activePath,
  open,
  onToggle,
}: {
  module: DashboardModule;
  /** The active nav page of this module, if the current route is in it. */
  activePath: string | null;
  open: boolean;
  onToggle: () => void;
}) {
  const pages = m.pages.filter((p) => !p.hidden);
  const Icon = m.icon;

  // A module with a single page is a plain link.
  if (pages.length === 1) {
    const p = pages[0]!;
    const on = activePath === p.path;
    return (
      <Link to={p.path} activeOptions={exact} className={itemClass(on)} aria-current={on ? "page" : undefined}>
        <Icon className="size-4 shrink-0" strokeWidth={1.75} />
        <span className="min-w-0 flex-1 truncate">{m.label}</span>
      </Link>
    );
  }
  return (
    <div className="flex flex-col gap-px">
      <button onClick={onToggle} className={cn(itemClass(false), activePath && "text-fg")} aria-expanded={open}>
        <Icon className={cn("size-4 shrink-0", activePath && "text-accent")} strokeWidth={1.75} />
        <span className="min-w-0 flex-1 truncate text-left">{m.label}</span>
        <ChevronDown className={cn("size-3.5 shrink-0 transition-transform", !open && "-rotate-90")} />
      </button>
      {open &&
        pages.map((p) => {
          const on = activePath === p.path;
          return (
            <Link key={p.path} to={p.path} activeOptions={exact} className={cn(itemClass(on), "pl-8")} aria-current={on ? "page" : undefined}>
              <span className="min-w-0 flex-1 truncate">{p.label}</span>
            </Link>
          );
        })}
    </div>
  );
}

function RailItem({ module: m, active }: { module: DashboardModule; active: boolean }) {
  const first = m.pages.find((p) => !p.hidden);
  if (!first) return null;
  const Icon = m.icon;
  return (
    <Link
      to={first.path}
      activeOptions={exact}
      title={m.label}
      aria-label={m.label}
      aria-current={active ? "page" : undefined}
      className={cn(itemClass(active), "justify-center px-0")}
    >
      <Icon className="size-4" strokeWidth={1.75} />
    </Link>
  );
}

function itemClass(active: boolean) {
  return cn(
    "flex h-7 w-full min-w-0 shrink-0 items-center gap-2 rounded-sm px-2 text-[13px] transition-colors",
    active ? "bg-raised text-fg" : "text-muted hover:text-fg hover:bg-hover",
  );
}
