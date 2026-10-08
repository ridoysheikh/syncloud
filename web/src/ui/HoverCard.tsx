import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { cn } from "./cn";

const OPEN_DELAY = 300;
const CLOSE_DELAY = 120;
const MARGIN = 8;

/** Whether the device can hover (no cards on touch screens). */
const canHover = () =>
  typeof window !== "undefined" &&
  window.matchMedia?.("(hover: hover) and (pointer: fine)").matches;

/**
 * A preview card for an inline reference (§10.1, Phase 16). It opens after a
 * short delay on hover or keyboard focus, renders in a portal next to the
 * trigger and flips to stay inside the viewport. The content mounts only while
 * open, so a table full of references costs nothing until one is hovered.
 */
export function HoverCard({
  trigger,
  children,
  className,
}: {
  /** The inline element; wrapped in a span that tracks hover and focus. */
  trigger: ReactNode;
  /** Rendered lazily while the card is open. */
  children: () => ReactNode;
  className?: string;
}) {
  const anchor = useRef<HTMLSpanElement>(null);
  const card = useRef<HTMLDivElement>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);

  const later = (v: boolean, ms: number) => {
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setOpen(v), ms);
  };
  const enter = () => canHover() && later(true, OPEN_DELAY);
  const leave = () => later(false, CLOSE_DELAY);
  useEffect(() => () => clearTimeout(timer.current), []);

  useLayoutEffect(() => {
    if (!open) {
      setPos(null);
      return;
    }
    const place = () => {
      const a = anchor.current?.getBoundingClientRect();
      const c = card.current?.getBoundingClientRect();
      if (!a || !c) return;
      const below = a.bottom + 4;
      const top =
        below + c.height + MARGIN > window.innerHeight &&
        a.top - c.height - 4 > MARGIN
          ? a.top - c.height - 4
          : below;
      const left = Math.max(
        MARGIN,
        Math.min(a.left, window.innerWidth - c.width - MARGIN),
      );
      setPos({ top, left });
    };
    place();
    const close = () => setOpen(false);
    window.addEventListener("scroll", close, true);
    window.addEventListener("resize", close);
    return () => {
      window.removeEventListener("scroll", close, true);
      window.removeEventListener("resize", close);
    };
  }, [open]);

  return (
    <>
      <span
        ref={anchor}
        className="inline-flex max-w-full min-w-0"
        onMouseEnter={enter}
        onMouseLeave={leave}
        onFocus={() => later(true, OPEN_DELAY)}
        onBlur={leave}
        onKeyDown={(e) => e.key === "Escape" && setOpen(false)}
      >
        {trigger}
      </span>
      {open &&
        createPortal(
          <div
            ref={card}
            role="tooltip"
            onMouseEnter={() => clearTimeout(timer.current)}
            onMouseLeave={leave}
            style={
              pos
                ? { top: pos.top, left: pos.left }
                : { top: 0, left: 0, visibility: "hidden" }
            }
            className={cn(
              "bg-raised border-line-strong text-fg fixed z-50 w-72 max-w-[calc(100vw-16px)] rounded-md border p-2.5 text-xs shadow-lg",
              className,
            )}
          >
            {children()}
          </div>,
          document.body,
        )}
    </>
  );
}

/** A label/value row inside a hover card. */
export function CardRow({ k, children }: { k: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 gap-2">
      <span className="text-muted w-16 shrink-0">{k}</span>
      <span className="min-w-0 break-words">{children}</span>
    </div>
  );
}
