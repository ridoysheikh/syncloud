import type { ReactNode } from "react";
import { cn } from "./cn";
import { PAGE_SIZE, Pagination, usePagination } from "./paging";

export interface Column<T> {
  header: string;
  cell: (row: T) => ReactNode;
  className?: string;
}

/**
 * Dense table: small text, ~30px rows, sticky header (§10.1). Rows are
 * paginated, pageSize (15) per page; a server-paged list passes
 * hasMore/onLoadMore and its next batch loads as the user pages toward it.
 */
export function DataTable<T>({
  columns,
  rows,
  rowKey,
  empty,
  pageSize = PAGE_SIZE,
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
  const { rows: visible, pager, paged } = usePagination(rows, { pageSize, hasMore, onLoadMore, loadingMore });
  return (
    <div>
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
      </div>
      {rows.length === 0 && empty}
      {paged && <Pagination {...pager} />}
    </div>
  );
}
