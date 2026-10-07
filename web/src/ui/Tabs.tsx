import type { ReactNode } from "react";
import { cn } from "./cn";

/** The 1px rule under a tab row, drawn as a shadow so it survives the scroller's clipping. */
export const tabRule = "shadow-[inset_0_-1px_0_var(--color-line)]";

/**
 * Underlined tab row. On narrow screens it scrolls sideways instead of
 * pushing the page wider than the viewport.
 */
export function Tabs<T extends string>({
  tabs,
  value,
  onChange,
  label = (t) => t,
  capitalize = true,
  className,
}: {
  tabs: readonly T[];
  value: T;
  onChange: (t: T) => void;
  label?: (t: T) => ReactNode;
  capitalize?: boolean;
  className?: string;
}) {
  return (
    <div
      role="tablist"
      className={cn(
        "flex min-w-0 gap-3 overflow-x-auto overflow-y-hidden text-xs [scrollbar-width:none]",
        tabRule,
        className,
      )}
    >
      {tabs.map((t) => (
        <button
          key={t}
          role="tab"
          aria-selected={value === t}
          onClick={() => onChange(t)}
          className={cn(
            "shrink-0 border-b-2 px-1 pb-1.5 whitespace-nowrap",
            capitalize && "capitalize",
            value === t
              ? "border-accent text-fg"
              : "text-muted hover:text-fg border-transparent",
          )}
        >
          {label(t)}
        </button>
      ))}
    </div>
  );
}
