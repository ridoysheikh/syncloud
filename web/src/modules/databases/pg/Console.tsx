import { useRef, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { History, Play, TriangleAlert } from "lucide-react";
import { api } from "@/lib/api";
import { usePgDatabases, usePgRoles, type PgQueryResult } from "@/lib/pg";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Toggle } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { errText, ResultGrid, inlineSelect } from "./shared";

const HISTORY_KEY = "syncloud.pg.history";

function loadHistory(): string[] {
  try {
    return JSON.parse(localStorage.getItem(HISTORY_KEY) ?? "[]") as string[];
  } catch {
    return [];
  }
}

function saveHistory(h: string[]) {
  try {
    localStorage.setItem(HISTORY_KEY, JSON.stringify(h.slice(0, 50)));
  } catch {
    // private windows and blocked storage: history is a convenience
  }
}

/** Line and column of a 1-based character position. */
function lineCol(text: string, pos: number) {
  const before = text.slice(0, Math.max(0, pos - 1));
  const lines = before.split("\n");
  return {
    line: lines.length,
    col: (lines[lines.length - 1] ?? "").length + 1,
  };
}

/** SQL console: read-only by default, as app or a role it may become. */
export function PgConsole({
  path,
  db,
  setDb,
}: {
  path: string;
  db: string;
  setDb: (db: string) => void;
}) {
  const dbs = usePgDatabases(path);
  const roles = usePgRoles(path);
  const [sql, setSql] = useState("SELECT now(), current_user, version();");
  const [role, setRole] = useState("app");
  const [write, setWrite] = useState(false);
  const [history, setHistory] = useState<string[]>(loadHistory);
  const area = useRef<HTMLTextAreaElement>(null);
  const run = useMutation({
    mutationFn: (text: string) =>
      api<PgQueryResult>("POST", `${path}/pg/${write ? "execute" : "query"}`, {
        database: db,
        sql: text,
        role,
      }),
    onSuccess: (_, text) => {
      const h = [text, ...history.filter((x) => x !== text)].slice(0, 50);
      setHistory(h);
      saveHistory(h);
    },
  });
  const submit = () => {
    const el = area.current;
    // Run the selection when there is one, like most SQL tools.
    const text =
      el && el.selectionEnd > el.selectionStart
        ? sql.slice(el.selectionStart, el.selectionEnd)
        : sql;
    if (text.trim()) run.mutate(text);
  };
  const r = run.data;
  const runAs = (roles.data ?? [])
    .filter((x) => !x.protected)
    .map((x) => x.name);
  const err = r?.error;
  const errAt = err?.position
    ? lineCol(run.variables ?? "", err.position)
    : null;

  return (
    <div className={cn("flex flex-col", gap)}>
      <Panel
        title="SQL"
        flush
        actions={
          <select
            className={cn(inlineSelect, "max-w-56")}
            value=""
            onChange={(e) => e.target.value && setSql(e.target.value)}
            aria-label="History"
            title="Recent queries (this browser)"
          >
            <option value="">History ({history.length})</option>
            {history.map((h, i) => (
              <option key={i} value={h}>
                {h.replace(/\s+/g, " ").slice(0, 80)}
              </option>
            ))}
          </select>
        }
      >
        <div className="border-line flex flex-wrap items-center gap-2 border-b p-2 text-xs">
          <label className="flex items-center gap-1.5">
            <span className="text-muted whitespace-nowrap">Database</span>
            <select
              className={cn(inlineSelect, "w-40")}
              value={db}
              onChange={(e) => setDb(e.target.value)}
            >
              {(dbs.data ?? []).map((d) => (
                <option key={d.name}>{d.name}</option>
              ))}
            </select>
          </label>
          <label className="flex items-center gap-1.5">
            <span className="text-muted whitespace-nowrap">Run as</span>
            <select
              className={cn(inlineSelect, "w-40")}
              value={role}
              onChange={(e) => setRole(e.target.value)}
            >
              {runAs.map((n) => (
                <option key={n}>{n}</option>
              ))}
            </select>
          </label>
          <Toggle
            checked={write}
            onChange={setWrite}
            label={write ? "Writes allowed (audited)" : "Read-only"}
          />
          <span className="flex-1" />
          <span className="text-faint hidden sm:inline">
            Ctrl+Enter runs the selection or everything
          </span>
          <Button variant="primary" onClick={submit} disabled={run.isPending}>
            <Play className="size-3.5" /> {run.isPending ? "Running…" : "Run"}
          </Button>
        </div>
        <textarea
          ref={area}
          value={sql}
          onChange={(e) => setSql(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
              e.preventDefault();
              submit();
            }
          }}
          spellCheck={false}
          aria-label="SQL"
          className={cn(
            "bg-bg block min-h-48 w-full resize-y p-2 font-mono text-xs leading-relaxed outline-none",
            write && "shadow-[inset_3px_0_0_var(--color-warn)]",
          )}
        />
      </Panel>
      {write && (
        <Alert tone="warn">
          <span className="flex items-center gap-1.5">
            <TriangleAlert className="size-3.5" /> Statements run and commit as{" "}
            <span className="font-mono">{role}</span>; the text is recorded in
            the audit log. A transaction left open is rolled back.
          </span>
        </Alert>
      )}
      {run.error && <Alert>{errText(run.error)}</Alert>}
      {r?.results.map((s, i) => (
        <StatementResult key={i} n={i} s={s} />
      ))}
      {err && (
        <Alert>
          <div className="flex flex-col gap-0.5">
            <span>
              Statement {err.statement + 1}: {err.message}
              {err.code && <span className="text-faint"> [{err.code}]</span>}
              {errAt && (
                <span className="text-faint">
                  {" "}
                  at line {errAt.line}, column {errAt.col}
                </span>
              )}
            </span>
            {err.detail && <span>Detail: {err.detail}</span>}
            {err.hint && <span>Hint: {err.hint}</span>}
            {r && r.readOnly && err.code === "25006" && (
              <span className="text-faint">
                Turn on “Writes allowed” to change data.
              </span>
            )}
          </div>
        </Alert>
      )}
      {r?.rolledBack && (
        <Alert tone="warn">
          A transaction was left open and has been rolled back.
        </Alert>
      )}
      {!r && !run.isPending && (
        <p className="text-faint flex items-center gap-1.5 text-xs">
          <History className="size-3.5" /> Runs on the primary through the
          platform, as {role}; several statements run in order. EXPLAIN (FORMAT
          JSON) shows a plan tree.
        </p>
      )}
    </div>
  );
}

function StatementResult({
  n,
  s,
}: {
  n: number;
  s: PgQueryResult["results"][number];
}) {
  const plan =
    s.columns.length === 1 &&
    s.columns[0]?.name === "QUERY PLAN" &&
    s.columns[0]?.type === "json"
      ? parsePlan(s.rows[0]?.[0])
      : null;
  return (
    <Panel
      flush
      title={
        <span className="normal-case">
          <span className="text-faint">#{n + 1}</span> {s.tag}
          <span className="text-faint">
            {" "}
            · {s.durationMs.toFixed(1)} ms
            {s.columns.length > 0 &&
              ` · ${s.rows.length} row${s.rows.length === 1 ? "" : "s"}${s.truncated ? " shown (more not shown)" : ""}`}
          </span>
        </span>
      }
    >
      {plan ? (
        <div className="p-2 text-xs">
          <PlanNode node={plan} />
        </div>
      ) : s.columns.length > 0 ? (
        <ResultGrid columns={s.columns} rows={s.rows} />
      ) : null}
    </Panel>
  );
}

interface Plan {
  "Node Type": string;
  "Relation Name"?: string;
  "Index Name"?: string;
  "Total Cost"?: number;
  "Plan Rows"?: number;
  "Actual Total Time"?: number;
  "Actual Rows"?: number;
  "Actual Loops"?: number;
  Filter?: string;
  "Index Cond"?: string;
  "Hash Cond"?: string;
  "Join Type"?: string;
  Plans?: Plan[];
}

function parsePlan(v: string | null | undefined): Plan | null {
  try {
    const j = JSON.parse(v ?? "") as { Plan: Plan }[];
    return j[0]?.Plan ?? null;
  } catch {
    return null;
  }
}

function PlanNode({ node, depth = 0 }: { node: Plan; depth?: number }) {
  const cond = node["Index Cond"] ?? node["Hash Cond"] ?? node.Filter;
  return (
    <div
      style={{ marginLeft: depth ? 16 : 0 }}
      className={cn(depth > 0 && "border-line border-l pl-2")}
    >
      <div className="flex flex-wrap items-baseline gap-x-2 py-0.5">
        <span className="font-medium">
          {node["Join Type"] ? `${node["Join Type"]} ` : ""}
          {node["Node Type"]}
        </span>
        {node["Relation Name"] && (
          <span className="font-mono">on {node["Relation Name"]}</span>
        )}
        {node["Index Name"] && (
          <span className="font-mono">using {node["Index Name"]}</span>
        )}
        <span className="text-faint">
          cost {node["Total Cost"]?.toFixed(1)} · est. {node["Plan Rows"]} rows
          {node["Actual Total Time"] !== undefined &&
            ` · actual ${node["Actual Total Time"].toFixed(2)} ms, ${node["Actual Rows"]} rows × ${node["Actual Loops"]}`}
        </span>
      </div>
      {cond && <div className="text-muted font-mono">{cond}</div>}
      {node.Plans?.map((p, i) => (
        <PlanNode key={i} node={p} depth={depth + 1} />
      ))}
    </div>
  );
}
