import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

/**
 * A dashboard feature area (§10.1). Modules declare their nav entries and
 * pages; the shell builds the side nav and routes from the registry, so new
 * areas plug in without touching the shell.
 */
export interface DashboardModule {
  id: string;
  label: string;
  icon: LucideIcon;
  /** Position in the side nav (ascending). */
  order: number;
  /** IAM action needed to see this module. Enforced once IAM lands (Phase 7). */
  permission?: string;
  pages: ModulePage[];
}

export interface ModulePage {
  /** Absolute path, e.g. "/compute/nodes". */
  path: string;
  label: string;
  component: () => ReactNode;
  /** Hide from the side nav (detail pages). */
  hidden?: boolean;
}
