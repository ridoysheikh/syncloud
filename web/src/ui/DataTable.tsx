import { useState, type ReactNode } from "react";
import { cn } from "./cn";
import { LoadMore } from "./LoadMore";

export interface Column<T> {
  header: string;
  cell: (row: T) => ReactNode;
  className?: string;
}

/**
 * Dense table: small text, ~30px rows, sticky header (§10.1). Rows render
 * lazily, pageSize at a time as the end scrolls into view; a server-paged
 * list passes hasMore/onLoadMore to fetch its next page the same way.
 */
export function DataTable<T>({
  columns,
  rows,
  rowKey,
  empty,
  pageSize = 50,
  hasMore = false,
  onLoadMore,
  loadingMore,
}: {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string;
  empty?: ReactNode;
  pageSize?: number;
  /** The server has more rows (cursor pagination). */
  hasMore?: boolean;
  onLoadMore?: () => void;
  loadingMore?: boolean;
}) {
  const [shown, setShown] = useState(pageSize);
  const visible = rows.length > shown ? rows.slice(0, shown) : rows;
  const moreHere = rows.length > shown;
  const moreThere = !moreHere && hasMore && !!onLoadMore;
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-xs">
        <thead className="bg-surface sticky top-0">
          <tr className="border-line border-b">
            {columns.map((c) => (
              <th key={c.header} className={cn("text-muted h-7 px-2 text-left font-medium whitespace-nowrap md:px-3", c.className)}>
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {visible.map((r) => (
            <tr key={rowKey(r)} className="border-line hover:bg-hover/50 h-8 border-b last:border-b-0">
              {columns.map((c) => (
                <td key={c.header} className={cn("px-2 whitespace-nowrap md:px-3", c.className)}>
                  {c.cell(r)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length === 0 && empty}
      <LoadMore
        more={moreHere || moreThere}
        loading={moreThere && loadingMore}
        onMore={() => (moreHere ? setShown((n) => n + pageSize) : onLoadMore?.())}
        label={moreHere ? `Show ${Math.min(pageSize, rows.length - shown)} more` : "Load more"}
        hint={moreHere ? `${shown} of ${rows.length}${hasMore ? "+" : ""}` : undefined}
      />
    </div>
  );
}
