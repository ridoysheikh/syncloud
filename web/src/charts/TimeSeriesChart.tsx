import { useEffect, useImperativeHandle, useRef, type Ref } from "react";
import * as echarts from "echarts/core";
import { LineChart } from "echarts/charts";
import { GridComponent, TooltipComponent, LegendComponent, AxisPointerComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import { registerTheme, THEME } from "./theme";

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, AxisPointerComponent, CanvasRenderer]);

export interface SeriesDef {
  name: string;
  /** Fill under the line with low opacity. */
  area?: boolean;
}

export interface TimeSeriesHandle {
  /** Append one point per series at time t (ms). Updates the chart without re-rendering React. */
  append(t: number, values: number[]): void;
}

/**
 * A live line chart. Data is pushed imperatively through the ref so streaming
 * updates never re-render the React tree (§10.1). Keeps a rolling window.
 */
export function TimeSeriesChart({
  ref,
  series,
  height = 160,
  windowSize = 150,
  unit,
}: {
  ref?: Ref<TimeSeriesHandle>;
  series: SeriesDef[];
  height?: number;
  windowSize?: number;
  unit?: string;
}) {
  const el = useRef<HTMLDivElement>(null);
  const chart = useRef<echarts.ECharts | null>(null);
  const data = useRef<[number, number][][]>(series.map(() => []));

  useEffect(() => {
    registerTheme();
    const c = echarts.init(el.current!, THEME, { renderer: "canvas" });
    chart.current = c;
    c.setOption({
      legend: { show: series.length > 1 },
      grid: { top: series.length > 1 ? 22 : 12 },
      tooltip: {
        trigger: "axis",
        valueFormatter: (v: unknown) => (typeof v === "number" ? `${+v.toFixed(2)}${unit ? ` ${unit}` : ""}` : String(v)),
      },
      xAxis: { type: "time" },
      yAxis: { type: "value", splitNumber: 3, scale: true },
      series: series.map((s, i) => ({
        name: s.name,
        type: "line",
        showSymbol: false,
        areaStyle: s.area ? { opacity: 0.08 } : undefined,
        data: data.current[i],
      })),
    });
    const ro = new ResizeObserver(() => c.resize());
    ro.observe(el.current!);
    return () => {
      ro.disconnect();
      c.dispose();
      chart.current = null;
    };
    // Series definitions are fixed for the life of the chart.
  }, []);

  useImperativeHandle(ref, () => ({
    append(t, values) {
      data.current.forEach((d, i) => {
        d.push([t, values[i] ?? NaN]);
        if (d.length > windowSize) d.shift();
      });
      chart.current?.setOption({ series: data.current.map((d) => ({ data: d })) });
    },
  }));

  return <div ref={el} style={{ height }} className="w-full" />;
}
