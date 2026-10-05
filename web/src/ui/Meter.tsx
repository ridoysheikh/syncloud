import { cn } from "./cn";

/** Thin usage bar with a value label; turns amber at 75% and red at 90%. */
export function Meter({ value, label, className }: { value: number; label?: string; className?: string }) {
  const v = Math.max(0, Math.min(100, value));
  return (
    <div className={cn("flex min-w-24 items-center gap-1.5", className)}>
      <div className="bg-raised h-1 flex-1 overflow-hidden rounded-sm">
        <div
          className={cn("h-full rounded-sm", v >= 90 ? "bg-bad" : v >= 75 ? "bg-warn" : "bg-accent")}
          style={{ width: `${v}%` }}
        />
      </div>
      <span className="text-muted w-20 shrink-0 text-right font-mono tabular-nums">{label ?? `${v.toFixed(0)}%`}</span>
    </div>
  );
}
