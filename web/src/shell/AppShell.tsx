import { useEffect, useState } from "react";
import { Outlet } from "@tanstack/react-router";
import { useSuspenseQuery } from "@tanstack/react-query";
import { X } from "lucide-react";
import { meQuery } from "@/lib/auth";
import { startStream } from "@/lib/stream";
import { readPref, writePref } from "@/lib/storage";
import { ShellPanel } from "@/modules/api/ApiPage";
import { IconButton } from "@/ui/controls";
import { pad } from "@/ui/cn";
import { Header } from "./Header";
import { SideNav } from "./SideNav";

/** Signed-in layout: header, modular side nav, page, bottom drawer (§10). */
export function AppShell() {
  const { data: user } = useSuspenseQuery(meQuery);
  const [collapsed, setCollapsed] = useState(() => readPref("nav.collapsed", false));
  const [drawer, setDrawer] = useState(false);

  useEffect(() => {
    startStream();
  }, []);

  if (!user) return null; // the route guard redirects before this renders

  return (
    <div className="flex h-full flex-col">
      <Header user={user} drawerOpen={drawer} onToggleDrawer={() => setDrawer((d) => !d)} />
      <div className="flex min-h-0 flex-1">
        <SideNav
          collapsed={collapsed}
          onToggle={() => {
            setCollapsed(!collapsed);
            writePref("nav.collapsed", !collapsed);
          }}
        />
        <div className="flex min-w-0 flex-1 flex-col">
          <main className={`min-h-0 flex-1 overflow-y-auto ${pad}`}>
            <Outlet />
          </main>
          {drawer && (
            <section className="bg-surface border-line max-h-[50vh] shrink-0 overflow-y-auto border-t" aria-label="Bottom drawer">
              <div className="border-line flex h-8 items-center justify-between border-b px-2">
                <span className="text-muted text-xs font-medium tracking-wide uppercase">Cloud Shell</span>
                <IconButton label="Close drawer" onClick={() => setDrawer(false)}>
                  <X className="size-3.5" />
                </IconButton>
              </div>
              <div className="p-1.5">
                <ShellPanel height="h-[34vh]" />
              </div>
            </section>
          )}
        </div>
      </div>
    </div>
  );
}
