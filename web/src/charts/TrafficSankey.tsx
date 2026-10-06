import { useEffect, useRef } from "react";
import * as echarts from "echarts/core";
import { SankeyChart } from "echarts/charts";
import { TooltipComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import { registerTheme, THEME } from "./theme";

echarts.use([SankeyChart, TooltipComponent, CanvasRenderer]);

export interface SankeyNode {
  name: string;
  label: string;
  color: string;
  /** Shown in the tooltip. */
  detail: string;
}

export interface SankeyLink {
  source: string;
  target: string;
  /** Requests per second (drawn with a minimum width so idle paths show). */
  value: number;
}

const css = (n: string) =>
  getComputedStyle(document.documentElement).getPropertyValue(n).trim();

/** Hostname → service → task flows, sized by requests per second (§5.7). */
export function TrafficSankey({
  nodes,
  links,
  height,
  onClick,
}: {
  nodes: SankeyNode[];
  links: SankeyLink[];
  height: number;
  onClick?: (name: string) => void;
}) {
  const el = useRef<HTMLDivElement>(null);
  const chart = useRef<echarts.ECharts | null>(null);
  const click = useRef(onClick);
  click.current = onClick;

  useEffect(() => {
    registerTheme();
    const c = echarts.init(el.current!, THEME, { renderer: "canvas" });
    chart.current = c;
    c.on("click", (p) => {
      const d = p.data as { name?: string } | undefined;
      if (p.dataType === "node" && d?.name) click.current?.(d.name);
    });
    const ro = new ResizeObserver(() => c.resize());
    ro.observe(el.current!);
    return () => {
      ro.disconnect();
      c.dispose();
      chart.current = null;
    };
  }, []);

  useEffect(() => {
    const byName = new Map(nodes.map((n) => [n.name, n]));
    // Idle paths still get a thin band; the tooltip shows the real rate.
    const floor = Math.max(...links.map((l) => l.value), 0.01) * 0.04;
    chart.current?.setOption(
      {
        tooltip: {
          trigger: "item",
          formatter: (p: {
            dataType: string;
            data: { name?: string; source?: string; target?: string };
            value: number;
          }) => {
            if (p.dataType === "node")
              return byName.get(p.data.name!)?.detail ?? "";
            const real = links.find(
              (l) => l.source === p.data.source && l.target === p.data.target,
            );
            return `${byName.get(p.data.source!)?.label} → ${byName.get(p.data.target!)?.label}<br/>${(real?.value ?? 0).toFixed(2)} req/s`;
          },
        },
        series: [
          {
            type: "sankey",
            left: 8,
            right: 140,
            top: 8,
            bottom: 8,
            nodeWidth: 10,
            nodeGap: 10,
            draggable: false,
            emphasis: { focus: "adjacency" },
            label: {
              color: css("--color-muted"),
              fontSize: 11,
              formatter: (p: { name: string }) =>
                byName.get(p.name)?.label ?? p.name,
            },
            lineStyle: { color: "gradient", opacity: 0.25, curveness: 0.5 },
            data: nodes.map((n) => ({
              name: n.name,
              itemStyle: { color: n.color, borderWidth: 0 },
            })),
            links: links.map((l) => ({
              ...l,
              value: Math.max(l.value, floor),
            })),
          },
        ],
      },
      { notMerge: true },
    );
  }, [nodes, links]);

  return <div ref={el} style={{ height }} className="w-full cursor-pointer" />;
}
