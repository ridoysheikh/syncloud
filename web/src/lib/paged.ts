import { useInfiniteQuery } from "@tanstack/react-query";
import { api } from "./api";
import { LAZY_BATCH } from "../ui/paging";

/** A page of a cursor-paginated history list (§14). */
export interface Page<T> {
  items: T[];
  /** Cursor of the next page; empty at the end. */
  next?: string;
}

/**
 * Loads a history list LAZY_BATCH rows at a time (?limit=&before=). Spread
 * `table` into a DataTable to fetch the next batch as the user pages on.
 */
export function usePaged<T>(
  key: readonly unknown[],
  path: string,
  opts: {
    limit?: number;
    /** A number, or one computed from the items loaded so far. */
    refetchInterval?: number | ((items: T[]) => number | false);
    enabled?: boolean;
  } = {},
) {
  const limit = opts.limit ?? LAZY_BATCH;
  const q = useInfiniteQuery({
    queryKey: [...key, "paged", limit],
    queryFn: ({ pageParam }) => {
      const sep = path.includes("?") ? "&" : "?";
      const before = pageParam
        ? `&before=${encodeURIComponent(pageParam)}`
        : "";
      return api<Page<T>>("GET", `${path}${sep}limit=${limit}${before}`);
    },
    initialPageParam: "",
    getNextPageParam: (last) => last.next || undefined,
    refetchInterval:
      typeof opts.refetchInterval === "function"
        ? (query) =>
            (opts.refetchInterval as (items: T[]) => number | false)(
              query.state.data?.pages.flatMap((p) => p.items) ?? [],
            )
        : opts.refetchInterval,
    enabled: opts.enabled,
  });
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];
  const hasMore = !!q.hasNextPage;
  const loadMore = () => void q.fetchNextPage({ cancelRefetch: false });
  return {
    ...q,
    items,
    hasMore,
    loadMore,
    table: { hasMore, onLoadMore: loadMore, loadingMore: q.isFetchingNextPage },
  };
}
