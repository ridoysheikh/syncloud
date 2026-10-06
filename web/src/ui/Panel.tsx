import { createContext, useContext, type ReactNode } from "react";
import { cn, pad } from "./cn";

/** True inside a Panel's body: nested surfaces drop their own padding. */
export const NestedContext = createContext(false);

/** Whether the caller renders inside another Panel. */
export function useNested() {
  return useContext(NestedContext);
}

/**
 * Bordered surface for grouping content. Separated by 1px lines, not shadows.
 * Inside another Panel it becomes a plain titled subsection: no border, no
 * background and no padding of its own, so padding never stacks.
 */
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
  const nested = useNested();
  return (
    <section className={cn("min-w-0", nested ? "flex flex-col gap-1" : "bg-surface border-line rounded-md border", className)}>
      {(title || actions) && (
        <header
          className={cn(
            "flex items-center justify-between gap-2",
            nested ? "h-6" : "border-line h-8 border-b px-2 md:px-3",
          )}
        >
          <h2 className="text-muted truncate text-xs font-medium tracking-wide uppercase">{title}</h2>
          {actions && <div className="flex items-center gap-1">{actions}</div>}
        </header>
      )}
      <NestedContext.Provider value={!flush || nested}>
        <div className={flush || nested ? undefined : pad}>{children}</div>
      </NestedContext.Provider>
    </section>
  );
}
