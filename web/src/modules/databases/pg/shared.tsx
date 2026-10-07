import { useState } from "react";
import {
  Binary,
  Braces,
  Eye,
  FileStack,
  FunctionSquare,
  Hash,
  Layers,
  Shapes,
  Table2,
  type LucideIcon,
} from "lucide-react";
import { ApiError } from "@/lib/api";
import type { PgObjectKind } from "@/lib/pg";
import { cn } from "@/ui/cn";

/** A compact select for toolbars and inline forms (selectClass is full width). */
export const inlineSelect =
  "bg-bg border-line-strong focus:border-line-accent h-7 rounded-input border px-1.5 text-xs outline-none";

export const errText = (e: unknown) =>
  e instanceof ApiError ? e.message : "Request failed";

export const kindIcon: Record<PgObjectKind, LucideIcon> = {
  table: Table2,
  partitioned: Layers,
  view: Eye,
  matview: FileStack,
  foreign: Binary,
  sequence: Hash,
  function: FunctionSquare,
  procedure: FunctionSquare,
  aggregate: Braces,
  type: Shapes,
};

export const kindLabel: Record<PgObjectKind, string> = {
  table: "Tables",
  partitioned: "Partitioned tables",
  view: "Views",
  matview: "Materialized views",
  foreign: "Foreign tables",
  sequence: "Sequences",
  function: "Functions",
  procedure: "Procedures",
  aggregate: "Aggregates",
  type: "Types",
};

export const kindOrder: PgObjectKind[] = [
  "table",
  "partitioned",
  "view",
  "matview",
  "foreign",
  "sequence",
  "function",
  "procedure",
  "aggregate",
  "type",
];

/** Relations whose rows can be browsed. */
export const browsable = (k: PgObjectKind) =>
  ["table", "partitioned", "view", "matview", "foreign"].includes(k);

/** One value from PostgreSQL's text form; NULL is shown apart from ''. */
export function Cell({ v }: { v: string | null }) {
  if (v === null) return <span className="text-faint italic">NULL</span>;
  if (v === "") return <span className="text-faint">''</span>;
  const short = v.length > 120 ? v.slice(0, 120) + "…" : v;
  return <span title={v.length > 120 ? v : undefined}>{short}</span>;
}

/** A grid of query results, with an optional sortable header. */
export function ResultGrid({
  columns,
  rows,
  sort,
  onSort,
  offset = 0,
}: {
  columns: { name: string; type: string }[];
  rows: (string | null)[][];
  sort?: { column: string; desc: boolean };
  onSort?: (column: string) => void;
  offset?: number;
}) {
  const [wrap, setWrap] = useState(false);
  return (
    <div className="max-h-[60vh] overflow-auto">
      <table className="w-full border-collapse font-mono text-xs">
        <thead className="bg-surface sticky top-0 z-10">
          <tr className="border-line border-b">
            <th className="text-faint w-8 px-2 text-right font-normal">
              <button
                type="button"
                onClick={() => setWrap((w) => !w)}
                title={wrap ? "Truncate long values" : "Wrap long values"}
                className="hover:text-fg"
              >
                #
              </button>
            </th>
            {columns.map((c) => (
              <th
                key={c.name}
                className="h-7 px-2 text-left font-medium whitespace-nowrap"
              >
                <button
                  type="button"
                  disabled={!onSort}
                  onClick={() => onSort?.(c.name)}
                  className={cn(
                    "flex items-baseline gap-1",
                    onSort && "hover:text-accent",
                  )}
                >
                  <span>{c.name}</span>
                  {sort?.column === c.name && (
                    <span className="text-accent">{sort.desc ? "↓" : "↑"}</span>
                  )}
                  <span className="text-faint font-sans text-[10px] font-normal">
                    {c.type}
                  </span>
                </button>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr
              key={i}
              className="border-line hover:bg-hover/50 border-b last:border-b-0"
            >
              <td className="text-faint px-2 py-1 text-right align-top">
                {offset + i + 1}
              </td>
              {r.map((v, j) => (
                <td
                  key={j}
                  className={cn(
                    "px-2 py-1 align-top",
                    wrap
                      ? "break-all whitespace-pre-wrap"
                      : "whitespace-nowrap",
                  )}
                >
                  {wrap && v !== null ? v : <Cell v={v} />}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** A labelled value in an object header. */
export function Fact({
  label,
  value,
}: {
  label: string;
  value: React.ReactNode;
}) {
  return (
    <div className="flex min-w-0 flex-col">
      <span className="text-faint text-[10px] tracking-wide uppercase">
        {label}
      </span>
      <span className="truncate">{value}</span>
    </div>
  );
}

export function SqlBlock({ sql }: { sql: string }) {
  return (
    <pre className="bg-bg border-line overflow-x-auto rounded-sm border p-2 font-mono text-xs leading-relaxed whitespace-pre">
      {sql}
    </pre>
  );
}

export const ago = (iso?: string | null) => {
  if (!iso) return "never";
  const s = (Date.now() - new Date(iso).getTime()) / 1000;
  if (s < 60) return `${Math.max(0, Math.round(s))}s ago`;
  if (s < 3600) return `${Math.round(s / 60)}m ago`;
  if (s < 86400) return `${Math.round(s / 3600)}h ago`;
  return `${Math.round(s / 86400)}d ago`;
};

export const duration = (iso?: string | null) => {
  if (!iso) return "";
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 1) return `${Math.round(s * 1000)}ms`;
  if (s < 60) return `${s.toFixed(1)}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
  return `${Math.floor(s / 3600)}h ${Math.round((s % 3600) / 60)}m`;
};
