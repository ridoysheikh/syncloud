import type { ReactNode } from "react";
import { cn, pad } from "./cn";

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
  return (
    <div className={cn("bg-surface border-line min-w-0 rounded-md border", pad)}>
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
