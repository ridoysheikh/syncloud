import { createRootRoute, createRoute, createRouter, Outlet, redirect } from "@tanstack/react-router";
import { FileQuestion } from "lucide-react";
import { meQuery, queryClient, statusQuery } from "@/lib/auth";
import { modules } from "@/modules";
import { AppShell } from "@/shell/AppShell";
import { SetupPage } from "@/pages/SetupPage";
import { LoginPage } from "@/pages/LoginPage";
import { EmptyState } from "@/ui/EmptyState";
import { ErrorView } from "@/ui/ErrorBoundary";

const rootRoute = createRootRoute({
  component: Outlet,
  notFoundComponent: () => (
    <EmptyState icon={FileQuestion} title="Page not found">
      This page does not exist.
    </EmptyState>
  ),
});

const setupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/setup",
  component: SetupPage,
  beforeLoad: async () => {
    const status = await queryClient.ensureQueryData(statusQuery);
    if (!status.setupRequired) throw redirect({ to: "/login" });
  },
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  component: LoginPage,
  beforeLoad: async () => {
    const status = await queryClient.ensureQueryData(statusQuery);
    if (status.setupRequired) throw redirect({ to: "/setup" });
    if (await queryClient.ensureQueryData(meQuery)) throw redirect({ to: "/" });
  },
});

/** Pathless layout for everything that needs a signed-in user. */
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "app",
  component: AppShell,
  beforeLoad: async () => {
    const status = await queryClient.ensureQueryData(statusQuery);
    if (status.setupRequired) throw redirect({ to: "/setup" });
    const me = await queryClient.ensureQueryData(meQuery);
    if (!me) throw redirect({ to: "/login", search: { next: window.location.pathname + window.location.search } as never });
  },
});

// Routes come from the module registry (§10.1).
const moduleRoutes = modules.flatMap((m) =>
  m.pages.map((p) => createRoute({ getParentRoute: () => appRoute, path: p.path, component: p.component })),
);

const routeTree = rootRoute.addChildren([setupRoute, loginRoute, appRoute.addChildren(moduleRoutes)]);

export const router = createRouter({
  routeTree,
  defaultPreload: "intent",
  // A page that fails to render shows the error in place, inside the shell.
  defaultErrorComponent: ({ error, reset }) => <ErrorView error={error} onRetry={reset} />,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
