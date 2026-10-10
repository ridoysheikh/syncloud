import {
  useEffect,
  useLayoutEffect,
  useState,
  type CSSProperties,
  type ReactNode,
  type RefObject,
} from "react";
import { createPortal } from "react-dom";
import { cn } from "./cn";

const MARGIN = 8;
const GAP = 4;

/**
 * Where a floating element must render: inside the open modal <dialog> that
 * holds its anchor (the page behind a modal is inert and drawn under the
 * dialog's top layer), else the body.
 */
export function portalRoot(anchor: Element | null): Element {
  return anchor?.closest("dialog[open]") ?? document.body;
}

/**
 * The fixed position of a popup below its anchor, or above it when there is
 * more room there, kept inside the viewport. Follows the anchor while the page
 * scrolls. Null until measured (render the popup hidden until then).
 */
export function useAnchoredStyle(
  open: boolean,
  anchor: RefObject<HTMLElement | null>,
  popup: RefObject<HTMLElement | null>,
  maxHeight = 288,
): CSSProperties | null {
  const [style, setStyle] = useState<CSSProperties | null>(null);
  useLayoutEffect(() => {
    if (!open) {
      setStyle(null);
      return;
    }
    const place = () => {
      const a = anchor.current?.getBoundingClientRect();
      const c = popup.current?.getBoundingClientRect();
      if (!a || !c) return;
      const below = window.innerHeight - a.bottom - GAP - MARGIN;
      const above = a.top - GAP - MARGIN;
      const down = below >= Math.min(c.height, 160) || below >= above;
      const width = Math.max(c.width, a.width);
      setStyle({
        minWidth: a.width,
        maxHeight: Math.max(96, Math.min(maxHeight, down ? below : above)),
        left: Math.max(
          MARGIN,
          Math.min(a.left, window.innerWidth - width - MARGIN),
        ),
        ...(down
          ? { top: a.bottom + GAP }
          : { bottom: window.innerHeight - a.top + GAP }),
      });
    };
    place();
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    // A dialog re-centres when its content changes height: follow it.
    const ro = new ResizeObserver(place);
    const a = anchor.current;
    if (a) {
      ro.observe(a);
      const root = portalRoot(a);
      if (root !== document.body) ro.observe(root);
    }
    return () => {
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
      ro.disconnect();
    };
  }, [open, anchor, popup, maxHeight]);
  return style;
}

/** Calls onOutside on a press outside every given element. */
export function useOutsidePress(
  open: boolean,
  refs: RefObject<HTMLElement | null>[],
  onOutside: () => void,
) {
  useEffect(() => {
    if (!open) return;
    const down = (e: PointerEvent) => {
      const t = e.target as Node;
      if (!refs.some((r) => r.current?.contains(t))) onOutside();
    };
    document.addEventListener("pointerdown", down, true);
    return () => document.removeEventListener("pointerdown", down, true);
  });
}

/**
 * A floating panel under an anchor (select lists, suggestions). The caller
 * owns open state and keyboard handling; this places, portals and styles it.
 */
export function Popup({
  open,
  anchor,
  popup,
  onClose,
  children,
  className,
  maxHeight,
}: {
  open: boolean;
  anchor: RefObject<HTMLElement | null>;
  popup: RefObject<HTMLDivElement | null>;
  onClose: () => void;
  children: ReactNode;
  className?: string;
  maxHeight?: number;
}) {
  const style = useAnchoredStyle(open, anchor, popup, maxHeight);
  useOutsidePress(open, [anchor, popup], onClose);
  if (!open) return null;
  return createPortal(
    <div
      ref={popup}
      // Unmeasured, it is transparent rather than hidden so its search box can
      // take focus.
      style={style ?? { top: 0, left: 0, opacity: 0 }}
      className={cn(
        "bg-raised border-line-strong text-fg animate-pop-in fixed z-50 flex max-w-[min(24rem,calc(100vw-16px))] flex-col overflow-hidden rounded-md border shadow-lg",
        className,
      )}
    >
      {children}
    </div>,
    portalRoot(anchor.current),
  );
}
