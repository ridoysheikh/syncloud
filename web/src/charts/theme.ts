// The single SynCloud ECharts theme (§10.1): transparent, borderless, faint
// dashed split lines, muted small labels, thin lines, no symbols until hover.
import * as echarts from "echarts/core";

const css = (name: string) =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim();

export const THEME = "syncloud";

/**
 * Categorical series palette, assigned in this fixed order. Validated against
 * the dark surface (#111418) for lightness, chroma, colorblind separation
 * and contrast; status colors (ok/warn/bad) stay reserved for states.
 */
export const SERIES = [
  "#3987e5", // blue
  "#d95926", // orange
  "#199e70", // aqua
  "#c98500", // yellow
  "#d55181", // magenta
  "#008300", // green
  "#9085e9", // violet
  "#e66767", // red
];

export function palette() {
  return SERIES;
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
    // Narrow charts drop time labels that would collide instead of overprinting them.
    timeAxis: {
      ...axis,
      axisLabel: { ...axis.axisLabel, hideOverlap: true },
      splitLine: { show: false },
    },
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
