import {
  useCallback,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { cn } from "./cn";

/** The 1px rule under a tab row, drawn as a shadow so it survives the scroller's clipping. */
export const tabRule = "shadow-[inset_0_-1px_0_var(--color-line)]";

/**
 * Underlined tab row. It stays one row: when the tabs do not fit it scrolls
 * sideways (wheel, swipe or the arrows that appear at a clipped end), and
 * the selected tab is kept in view. One underline slides to the selection.
 */
export function Tabs<T extends string>({
  tabs,
  value,
  onChange,
  label = (t) => t,
  capitalize = true,
  className,
}: {
  tabs: readonly T[];
  value: T;
  onChange: (t: T) => void;
  label?: (t: T) => ReactNode;
  capitalize?: boolean;
  className?: string;
}) {
  const list = useRef<HTMLDivElement>(null);
  const [bar, setBar] = useState<{ left: number; width: number } | null>(null);
  const [more, setMore] = useState({ start: false, end: false });

  // Measuring (underline, arrows) follows layout and scrolling; revealing the
  // selection follows only a change of selection, so a strip the user has
  // scrolled stays where they put it.
  const measure = useCallback(() => {
    const el = list.current;
    if (!el) return;
    const on = el.querySelector<HTMLElement>("[aria-selected=true]");
    setBar(on ? { left: on.offsetLeft, width: on.offsetWidth } : null);
    const max = el.scrollWidth - el.clientWidth;
    // 1px of slack for sub-pixel widths.
    setMore({ start: el.scrollLeft > 1, end: max > 1 && el.scrollLeft < max - 1 });
  }, []);

  const reveal = useCallback((behavior: ScrollBehavior) => {
    const el = list.current;
    const on = el?.querySelector<HTMLElement>("[aria-selected=true]");
    if (!el || !on) return;
    // Scroll the strip itself: scrollIntoView would scroll the page too.
    const left = on.offsetLeft - 24;
    const right = on.offsetLeft + on.offsetWidth + 24;
    if (left < el.scrollLeft) el.scrollTo({ left, behavior });
    else if (right > el.scrollLeft + el.clientWidth)
      el.scrollTo({ left: right - el.clientWidth, behavior });
  }, []);

  const first = useRef(true);
  useLayoutEffect(() => {
    // Jump on first paint (a strip sliding in reads as a glitch), glide after.
    reveal(first.current ? "auto" : "smooth");
    first.current = false;
    measure();
  }, [value, reveal, measure]);

  useLayoutEffect(() => {
    const el = list.current;
    if (!el) return;
    // The strip resizing, or a label changing width (a count arriving).
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    Array.from(el.children).forEach((c) => ro.observe(c));
    let frame = 0;
    const onScroll = () => {
      if (!frame)
        frame = requestAnimationFrame(() => {
          frame = 0;
          measure();
        });
    };
    // A mouse wheel only scrolls vertically: map it onto the strip, but let
    // the page scroll on when the strip is at that end or does not overflow.
    // Not passive (React's onWheel is), so the page does not scroll as well.
    const onWheel = (e: globalThis.WheelEvent) => {
      if (Math.abs(e.deltaY) <= Math.abs(e.deltaX)) return;
      const max = el.scrollWidth - el.clientWidth;
      const next = el.scrollLeft + e.deltaY;
      if (max <= 1 || (next < 0 && el.scrollLeft <= 0) || (next > max && el.scrollLeft >= max)) return;
      e.preventDefault();
      el.scrollLeft = Math.max(0, Math.min(max, next));
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => {
      ro.disconnect();
      cancelAnimationFrame(frame);
      el.removeEventListener("scroll", onScroll);
      el.removeEventListener("wheel", onWheel);
    };
  }, [measure, tabs]);

  // Arrow keys move the selection, as in any tab list.
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const step = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
    const i = tabs.indexOf(value);
    const next =
      e.key === "Home" ? 0 : e.key === "End" ? tabs.length - 1 : step ? (i + step + tabs.length) % tabs.length : -1;
    if (next < 0 || !tabs[next]) return;
    e.preventDefault();
    onChange(tabs[next]);
    list.current?.querySelectorAll<HTMLElement>("[role=tab]")[next]?.focus({ preventScroll: true });
  };

  const page = (dir: 1 | -1) => {
    const el = list.current;
    // Most of a width, not all: a tab stays in view to keep the reader's place.
    el?.scrollBy({ left: dir * el.clientWidth * 0.75, behavior: "smooth" });
  };

  return (
    <div className={cn("relative flex min-w-0 items-stretch", tabRule, className)}>
      {more.start && <ScrollArrow dir={-1} onClick={() => page(-1)} />}
      <div
        ref={list}
        role="tablist"
        onKeyDown={onKeyDown}
        className="relative flex min-w-0 flex-1 gap-3 overflow-x-auto overflow-y-hidden overscroll-x-contain pr-4 text-xs [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
      >
        {tabs.map((t) => (
          <button
            key={t}
            type="button"
            role="tab"
            aria-selected={value === t}
            tabIndex={value === t ? 0 : -1}
            onClick={() => onChange(t)}
            className={cn(
              "shrink-0 px-1 pb-1.5 whitespace-nowrap transition-colors outline-none focus-visible:text-fg",
              capitalize && "capitalize",
              value === t ? "text-fg" : "text-muted hover:text-fg",
            )}
          >
            {label(t)}
          </button>
        ))}
        {bar && (
          <span
            aria-hidden
            className="bg-line-accent pointer-events-none absolute bottom-0 h-0.5 rounded-full transition-[left,width] duration-200 ease-out"
            style={{ left: bar.left, width: bar.width }}
          />
        )}
      </div>
      {more.end && <ScrollArrow dir={1} onClick={() => page(1)} />}
    </div>
  );
}

/** Shown only at an end with tabs clipped past it. */
function ScrollArrow({ dir, onClick }: { dir: 1 | -1; onClick: () => void }) {
  const Icon = dir < 0 ? ChevronLeft : ChevronRight;
  return (
    <button
      type="button"
      tabIndex={-1}
      aria-label={dir < 0 ? "Scroll tabs left" : "Scroll tabs right"}
      onClick={onClick}
      className={cn(
        "bg-bg text-muted hover:text-fg z-10 flex w-5 shrink-0 items-center justify-center pb-1.5 transition-colors",
        dir < 0 ? "pr-0.5" : "pl-0.5",
      )}
    >
      <Icon className="size-3.5" />
    </button>
  );
}
