import { useEffect, useRef } from "react";
import * as echarts from "echarts/core";
import { GraphChart } from "echarts/charts";
import { TooltipComponent } from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import { registerTheme, THEME } from "./theme";

echarts.use([GraphChart, TooltipComponent, CanvasRenderer]);

export interface GraphNode {
  id: string;
  label: string;
  sub: string;
  tone: "ok" | "warn" | "bad" | "neutral";
}

export interface GraphLink {
  source: string;
  target: string;
  tone: "ok" | "warn" | "bad";
  label: string;
}

const css = (name: string) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();

/** Mesh topology (§8.4): nodes on a circle, links colored by health. */
export function MeshGraph({ nodes, links, height = 320 }: { nodes: GraphNode[]; links: GraphLink[]; height?: number }) {
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
    };
  }, []);

  useEffect(() => {
    const color = (t: string) => css(`--color-${t}`);
    chart.current?.setOption({
      tooltip: { formatter: (p: { dataType: string; data: { tip?: string } }) => p.data.tip ?? "" },
      series: [
        {
          type: "graph",
          layout: "circular",
          circular: { rotateLabel: false },
          roam: true,
          symbolSize: 34,
          label: { show: true, position: "bottom", color: css("--color-fg"), fontSize: 11, formatter: "{b}" },
          edgeLabel: { show: true, fontSize: 9, color: css("--color-faint"), formatter: (p: { data: { label: string } }) => p.data.label },
          lineStyle: { width: 1.5, opacity: 0.9, curveness: 0.08 },
          data: nodes.map((n) => ({
            id: n.id,
            name: n.label,
            tip: `${n.label}<br/>${n.sub}`,
            itemStyle: { color: css("--color-raised"), borderColor: color(n.tone), borderWidth: 2 },
          })),
          links: links.map((l) => ({
            source: l.source,
            target: l.target,
            label: l.label,
            tip: l.label,
            lineStyle: { color: color(l.tone) },
          })),
        },
      ],
    });
  }, [nodes, links]);

  return <div ref={el} style={{ height }} className="w-full" />;
}
