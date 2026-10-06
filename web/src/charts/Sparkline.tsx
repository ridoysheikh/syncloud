/** A tiny inline latency chart (SVG, no ECharts: tables render many of them). */
export function Sparkline({
  points,
  width = 120,
  height = 22,
}: {
  points: { t: number; ms: number; ok: boolean }[];
  width?: number;
  height?: number;
}) {
  if (points.length < 2) return <span className="text-faint">—</span>;
  const t0 = points.at(0)?.t ?? 0;
  const t1 = points.at(-1)?.t || t0 + 1;
  const max = Math.max(...points.map((p) => p.ms), 1);
  const x = (t: number) => ((t - t0) / Math.max(t1 - t0, 1)) * (width - 2) + 1;
  const y = (ms: number) => height - 1 - (ms / max) * (height - 2);
  const d = points
    .map((p, i) => `${i ? "L" : "M"}${x(p.t).toFixed(1)},${y(p.ms).toFixed(1)}`)
    .join(" ");
  return (
    <svg width={width} height={height} className="overflow-visible">
      <path d={d} fill="none" stroke="var(--color-accent)" strokeWidth={1.2} />
      {points
        .filter((p) => !p.ok)
        .map((p) => (
          <circle
            key={p.t}
            cx={x(p.t)}
            cy={y(p.ms)}
            r={1.8}
            fill="var(--color-bad)"
          />
        ))}
    </svg>
  );
}
