import { cn } from "./cn";

/** The chart ranges the metrics API offers. */
export const ranges = ["15m", "1h", "6h", "24h", "7d"] as const;
export type Range = (typeof ranges)[number];

/** How often to refetch a chart of this range: short ranges move faster. */
export const refetchFor = (r: Range) =>
  r === "15m" || r === "1h" ? 15_000 : 60_000;

/** Segmented time-range control, shown above the charts it filters. */
export function RangePicker({
  range,
  setRange,
}: {
  range: Range;
  setRange: (r: Range) => void;
}) {
  return (
    <div
      role="group"
      aria-label="Time range"
      className="border-line flex shrink-0 overflow-hidden rounded-sm border"
    >
      {ranges.map((r) => (
        <button
          key={r}
          onClick={() => setRange(r)}
          aria-pressed={r === range}
          className={cn(
            "px-2 py-0.5 text-xs",
            r === range ? "bg-raised text-fg" : "text-muted hover:text-fg",
          )}
        >
          {r}
        </button>
      ))}
    </div>
  );
}
