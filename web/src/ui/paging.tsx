import { useEffect, useState, type ReactNode } from "react";
import { ChevronLeft, ChevronRight, Loader2 } from "lucide-react";
import { IconButton } from "./controls";
import { LoadMore } from "./LoadMore";
import { Select } from "@/ui/select";

/** Rows per table page. */
export const PAGE_SIZE = 15;
/** Items per lazy batch: list items rendered, or rows fetched from the server. */
export const LAZY_BATCH = 30;
const PAGE_SIZES = [15, 30, 50, 100];

/**
 * Pager for a table: range, page size and prev/next. `more` means the server
 * has rows beyond those loaded, so the total is shown as "N+".
 */
export function Pagination({
  page,
  pageSize,
  total,
  more = false,
  loading,
  onPage,
  onPageSize,
}: {
  page: number;
  pageSize: number;
  total: number;
  more?: boolean;
  loading?: boolean;
  onPage: (page: number) => void;
  onPageSize: (size: number) => void;
}) {
  const pages = Math.max(1, Math.ceil(total / pageSize));
  const from = total === 0 ? 0 : page * pageSize + 1;
  const to = Math.min(total, (page + 1) * pageSize);
  return (
    <div className="border-line text-muted flex flex-wrap items-center justify-end gap-x-3 gap-y-1 border-t px-2 py-1 text-xs md:px-3">
      <label className="flex items-center gap-1.5">
        Rows
        <Select
          value={pageSize}
          onChange={(v) => onPageSize(Number(v))}
          size="xs"
        >
          {PAGE_SIZES.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </Select>
      </label>
      <span className="tabular-nums">
        {from}–{to} of {total}
        {more ? "+" : ""}
      </span>
      <span className="flex items-center">
        {loading && <Loader2 className="text-faint mr-1 size-3.5 animate-spin" aria-label="Loading" />}
        <IconButton label="Previous page" disabled={page === 0} onClick={() => onPage(page - 1)} className="disabled:opacity-40">
          <ChevronLeft className="size-4" />
        </IconButton>
        <span className="min-w-12 text-center tabular-nums">
          {page + 1} / {pages}
          {more ? "+" : ""}
        </span>
        <IconButton
          label="Next page"
          disabled={page >= pages - 1 && !more}
          onClick={() => onPage(page + 1)}
          className="disabled:opacity-40"
        >
          <ChevronRight className="size-4" />
        </IconButton>
      </span>
    </div>
  );
}

/**
 * Paginates rows on the client. A server-paged list passes hasMore/onLoadMore
 * and the next batch is fetched while the user is one page from the end.
 */
export function usePagination<T>(
  rows: T[],
  {
    pageSize: initial = PAGE_SIZE,
    hasMore = false,
    onLoadMore,
    loadingMore = false,
  }: { pageSize?: number; hasMore?: boolean; onLoadMore?: () => void; loadingMore?: boolean } = {},
) {
  const [pageSize, setPageSize] = useState(initial);
  const [want, setPage] = useState(0);
  const more = hasMore && !!onLoadMore;
  const lastLoaded = Math.max(0, Math.ceil(rows.length / pageSize) - 1);
  // Stay on a page past the loaded rows only while its rows are coming.
  const page = more ? Math.min(want, lastLoaded + 1) : Math.min(want, lastLoaded);
  useEffect(() => {
    if (more && !loadingMore && (page + 2) * pageSize > rows.length) onLoadMore?.();
  }, [more, loadingMore, page, pageSize, rows.length, onLoadMore]);
  return {
    rows: rows.slice(page * pageSize, (page + 1) * pageSize),
    pager: {
      page,
      pageSize,
      total: rows.length,
      more,
      loading: more && loadingMore,
      onPage: setPage,
      onPageSize: (n: number) => {
        // Keep the first visible row on screen.
        setPage(Math.floor((page * pageSize) / n));
        setPageSize(n);
      },
    },
    /** Pagination is only worth showing past one page. */
    paged: rows.length > pageSize || more || page > 0,
  };
}

/**
 * Renders a long list LAZY_BATCH items at a time: map `shown`, and place
 * `more` after the list to reveal the next batch as it scrolls into view.
 * A server-paged list passes hasMore/onLoadMore to fetch once all are shown.
 */
export function useLazyList<T>(
  items: T[],
  {
    batch = LAZY_BATCH,
    hasMore = false,
    onLoadMore,
    loadingMore,
  }: { batch?: number; hasMore?: boolean; onLoadMore?: () => void; loadingMore?: boolean } = {},
) {
  const [count, setCount] = useState(batch);
  const shown = items.length > count ? items.slice(0, count) : items;
  const moreHere = items.length > count;
  const moreThere = !moreHere && hasMore && !!onLoadMore;
  const more = (
    <LoadMore
      more={moreHere || moreThere}
      loading={moreThere && loadingMore}
      onMore={() => (moreHere ? setCount((n) => n + batch) : onLoadMore?.())}
      label={moreHere ? `Show ${Math.min(batch, items.length - count)} more` : "Load more"}
      hint={moreHere ? `${count} of ${items.length}${hasMore ? "+" : ""}` : undefined}
    />
  );
  return { shown, more };
}

/** useLazyList as a component, for lists rendered inside a loop. */
export function LazyItems<T>({ items, children }: { items: T[]; children: (item: T) => ReactNode }) {
  const { shown, more } = useLazyList(items);
  return (
    <>
      {shown.map(children)}
      {more}
    </>
  );
}
