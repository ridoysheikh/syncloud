// The single SynCloud ECharts theme (§10.1): transparent, borderless, faint
// dashed split lines, muted small labels, thin lines, no symbols until hover.
import * as echarts from "echarts/core";

const css = (name: string) =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim();

export const THEME = "syncloud";

/** Series palette, in order. */
export function palette() {
  return [
    css("--color-accent"),
    "#a371f7",
    "#3fb9a0",
    css("--color-warn"),
    "#db61a2",
    css("--color-ok"),
  ];
}

let registered = false;

export function registerTheme() {
  if (registered) return;
  registered = true;
  const muted = css("--color-faint");
  const line = css("--color-line");
  const axis = {
    axisLine: { show: false },
    axisTick: { show: false },
    axisLabel: { color: muted, fontSize: 10 },
    splitLine: { lineStyle: { color: line, type: "dashed" } },
  };
  echarts.registerTheme(THEME, {
    color: palette(),
    backgroundColor: "transparent",
    textStyle: { fontFamily: css("--font-sans"), color: css("--color-muted") },
    grid: { left: 36, right: 8, top: 12, bottom: 20, containLabel: false },
    categoryAxis: axis,
    valueAxis: axis,
    timeAxis: { ...axis, splitLine: { show: false } },
    line: { symbol: "none", lineStyle: { width: 1.5 }, smooth: false },
    legend: {
      show: false,
      textStyle: { color: css("--color-muted"), fontSize: 10 },
      top: 0,
      itemWidth: 10,
      itemHeight: 2,
    },
    tooltip: {
      backgroundColor: css("--color-raised"),
      borderColor: css("--color-line-strong"),
      borderWidth: 1,
      padding: [4, 8],
      textStyle: { color: css("--color-fg"), fontSize: 11 },
      axisPointer: {
        lineStyle: { color: css("--color-line-strong") },
        crossStyle: { color: css("--color-line-strong") },
      },
    },
  });
}
