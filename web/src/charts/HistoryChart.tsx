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
}: {
  series: HistorySeries[];
  start: number;
  end: number;
  format: (v: number) => string;
  height?: number;
  area?: boolean;
  /** A dashed reference line, e.g. a memory limit. */
  markLine?: { value: number; label: string };
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
        grid: { top: series.length > 1 ? 24 : 12, left: 52 },
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
          axisLabel: { formatter: (v: number) => format(v) },
        },
        series: series.map((s, i) => ({
          name: s.name,
          type: "line",
          showSymbol: false,
          connectNulls: false,
          areaStyle: area ? { opacity: 0.06 } : undefined,
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
  }, [series, start, end, format, area, markLine]);

  return <div ref={el} style={{ height }} className="w-full" />;
}
