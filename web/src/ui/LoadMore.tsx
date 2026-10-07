import { useEffect, useRef } from "react";
import { Loader2 } from "lucide-react";
import { Button } from "./controls";

/**
 * The end of a lazily loaded list: it calls onMore when it scrolls into view
 * (and offers a button for when it does not), while there is more.
 */
export function LoadMore({
  more,
  loading,
  onMore,
  label = "Load more",
  hint,
}: {
  /** There is more to show. */
  more: boolean;
  loading?: boolean;
  onMore: () => void;
  label?: string;
  /** e.g. "50 of 420". */
  hint?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const cb = useRef(onMore);
  cb.current = onMore;
  useEffect(() => {
    const el = ref.current;
    if (!el || !more || loading) return;
    const io = new IntersectionObserver(
      (es) => {
        if (es.some((e) => e.isIntersecting)) cb.current();
      },
      { rootMargin: "200px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [more, loading]);
  if (!more && !hint) return null;
  return (
    <div
      ref={ref}
      className="text-faint flex items-center justify-center gap-2 px-2 py-1.5 text-xs"
    >
      {hint && <span>{hint}</span>}
      {more &&
        (loading ? (
          <Loader2 className="size-3.5 animate-spin" aria-label="Loading" />
        ) : (
          <Button variant="ghost" onClick={onMore}>
            {label}
          </Button>
        ))}
    </div>
  );
}
