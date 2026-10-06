import type { ReactNode } from "react";
import { cn, pad } from "./cn";
import { useNested } from "./Panel";

export function StatTile({
  label,
  value,
  unit,
  hint,
  tone,
}: {
  label: string;
  value: ReactNode;
  unit?: string;
  hint?: ReactNode;
  tone?: "ok" | "warn" | "bad";
}) {
  // Inside a Panel the tile is a plain figure with a rule, not a second card.
  const nested = useNested();
  return (
    <div className={cn("min-w-0", nested ? "border-line border-l-2 pl-2" : cn("bg-surface border-line rounded-md border", pad))}>
      <div className="text-muted truncate text-xs">{label}</div>
      <div
        className={cn(
          "mt-0.5 truncate font-mono text-lg leading-tight tabular-nums md:text-xl",
          tone === "ok" && "text-ok",
          tone === "warn" && "text-warn",
          tone === "bad" && "text-bad",
        )}
      >
        {value}
        {unit && <span className="text-muted ml-1 text-xs">{unit}</span>}
      </div>
      {hint && <div className="text-faint mt-0.5 truncate text-xs">{hint}</div>}
    </div>
  );
}
