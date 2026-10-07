import { useEffect, useRef } from "react";
import * as echarts from "echarts/core";
import { LineChart } from "echarts/charts";
import {
  GridComponent,
  TooltipComponent,
  LegendComponent,
  AxisPointerComponent,
} from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import { registerTheme, THEME } from "./theme";

echarts.use([
  LineChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  AxisPointerComponent,
  CanvasRenderer,
]);

export interface HistorySeries {
  name: string;
  /** [unix ms, value] */
  points: [number, number][];
}

/**
 * A line chart over a fixed time range (historical data, replaced on every
 * fetch). `format` renders values on the axis and in the tooltip.
 */
export function HistoryChart({
  series,
  start,
  end,
  format,
  height = 180,
  area,
  markLine,
  colors,
  stack,
  max,
  integer,
}: {
  series: HistorySeries[];
  start: number;
  end: number;
  format: (v: number) => string;
  height?: number;
  area?: boolean;
  /** A dashed reference line, e.g. a memory limit. */
  markLine?: { value: number; label: string };
  /** Fixed colors by series name (e.g. status classes). */
  colors?: Record<string, string>;
  /** Stack the series (areas add up to the total). */
  stack?: boolean;
  /** Fixed top of the value axis (e.g. 100 for percentages). */
  max?: number;
  /** Whole-number axis ticks (counts). */
  integer?: boolean;
}) {
  const el = useRef<HTMLDivElement>(null);
  const chart = useRef<echarts.ECharts | null>(null);

  useEffect(() => {
    registerTheme();
    const c = echarts.init(el.current!, THEME, { renderer: "canvas" });
    chart.current = c;
    const ro = new ResizeObserver(() => c.resize());
    ro.observe(el.current!);
    return () => {
      ro.disconnect();
      c.dispose();
      chart.current = null;
    };
  }, []);

  useEffect(() => {
    chart.current?.setOption(
      {
        legend: { show: series.length > 1, type: "scroll" },
        grid: { top: series.length > 1 ? 24 : 12, left: 52, right: 16 },
        tooltip: {
          trigger: "axis",
          valueFormatter: (v: unknown) =>
            typeof v === "number" ? format(v) : String(v),
        },
        xAxis: { type: "time", min: start, max: end },
        yAxis: {
          type: "value",
          splitNumber: 3,
          min: 0,
          // Room for the reference line (a memory total or limit) too.
          max:
            max ??
            (markLine
              ? (v: { max: number }) => Math.max(v.max, markLine.value) * 1.05
              : undefined),
          // A fixed top gets even quarters, so its label never crowds the last tick.
          interval: max !== undefined ? max / 4 : undefined,
          minInterval: integer ? 1 : undefined,
          axisLabel: { formatter: (v: number) => format(v) },
        },
        series: series.map((s, i) => ({
          name: s.name,
          type: "line",
          showSymbol: false,
          connectNulls: false,
          areaStyle:
            area || stack ? { opacity: stack ? 0.25 : 0.06 } : undefined,
          stack: stack ? "total" : undefined,
          color: colors?.[s.name],
          data: s.points,
          markLine:
            i === 0 && markLine
              ? {
                  silent: true,
                  symbol: "none",
                  lineStyle: { type: "dashed", opacity: 0.5 },
                  label: { formatter: markLine.label, fontSize: 10 },
                  data: [{ yAxis: markLine.value }],
                }
              : undefined,
        })),
      },
      { notMerge: true },
    );
  }, [series, start, end, format, area, markLine, colors, stack, max, integer]);

  return <div ref={el} style={{ height }} className="w-full" />;
}
