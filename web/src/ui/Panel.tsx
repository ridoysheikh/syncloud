import type { ReactNode } from "react";
import { cn, pad } from "./cn";

/** Bordered surface for grouping content. Separated by 1px lines, not shadows. */
export function Panel({
  title,
  actions,
  children,
  className,
  flush,
}: {
  title?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
  /** Remove body padding (for tables and charts that manage their own). */
  flush?: boolean;
}) {
  return (
    <section className={cn("bg-surface border-line min-w-0 rounded-md border", className)}>
      {(title || actions) && (
        <header className="border-line flex h-8 items-center justify-between gap-2 border-b px-2 md:px-3">
          <h2 className="text-muted truncate text-xs font-medium tracking-wide uppercase">{title}</h2>
          {actions && <div className="flex items-center gap-1">{actions}</div>}
        </header>
      )}
      <div className={flush ? undefined : pad}>{children}</div>
    </section>
  );
}
