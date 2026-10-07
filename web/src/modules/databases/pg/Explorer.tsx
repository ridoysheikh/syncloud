import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Database as DbIcon,
  FolderTree,
  Plus,
  RefreshCw,
  Search,
  X,
} from "lucide-react";
import { api } from "@/lib/api";
import { bytes } from "@/lib/nodes";
import {
  pgUrl,
  usePgDatabases,
  usePgExtensions,
  usePgObject,
  usePgSchema,
  type PgObject,
  type PgObjectRef,
  type PgRowFilter,
  type PgRows,
} from "@/lib/pg";
import { Panel } from "@/ui/Panel";
import { EmptyState } from "@/ui/EmptyState";
import { Tabs } from "@/ui/Tabs";
import { Alert, Button, IconButton, Input, Toggle } from "@/ui/controls";
import { DataTable } from "@/ui/DataTable";
import { cn, gap } from "@/ui/cn";
import { selectClass } from "../NewDatabaseWizard";
import { PrivilegesEditor } from "./Privileges";
import {
  ago,
  browsable,
  errText,
  Fact,
  kindIcon,
  kindLabel,
  kindOrder,
  ResultGrid,
  SqlBlock,
  inlineSelect,
} from "./shared";
import { confirmAction } from "@/ui/dialogs";

type Selection =
  | { kind: "database" }
  | { kind: "schema"; schema: string }
  | { kind: "object"; schema: string; object: PgObject };

/** Databases, schemas and objects of a PostgreSQL cluster. */
export function PgExplorer({
  path,
  db,
  setDb,
}: {
  path: string;
  db: string;
  setDb: (db: string) => void;
}) {
  const dbs = usePgDatabases(path);
  const [system, setSystem] = useState(false);
  const schema = usePgSchema(path, db, system);
  const [sel, setSel] = useState<Selection>({ kind: "database" });
  const [filter, setFilter] = useState("");
  const [open, setOpen] = useState<Record<string, boolean>>({ public: true });
  useEffect(() => setSel({ kind: "database" }), [db]);

  const tree = useMemo(() => {
    const f = filter.toLowerCase();
    return (schema.data ?? []).map((s) => ({
      ...s,
      objects: f
        ? s.objects.filter((o) => o.name.toLowerCase().includes(f))
        : s.objects,
    }));
  }, [schema.data, filter]);

  return (
    <div
      className={cn(
        "grid grid-cols-1 items-start lg:grid-cols-[18rem_1fr]",
        gap,
      )}
    >
      <Panel
        title="Objects"
        flush
        actions={
          <IconButton label="Reload" onClick={() => void schema.refetch()}>
            <RefreshCw
              className={cn("size-3.5", schema.isFetching && "animate-spin")}
            />
          </IconButton>
        }
      >
        <div className="border-line flex flex-col gap-1.5 border-b p-2">
          <select
            className={selectClass}
            value={db}
            onChange={(e) => setDb(e.target.value)}
            aria-label="Database"
          >
            {(dbs.data ?? []).map((d) => (
              <option key={d.name} value={d.name}>
                {d.name}
                {d.primary ? " (cluster database)" : ""}
              </option>
            ))}
          </select>
          <div className="relative">
            <Search className="text-faint absolute top-1/2 left-2 size-3.5 -translate-y-1/2" />
            <Input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Filter objects"
              className="h-7 pl-7"
            />
          </div>
          <Toggle
            checked={system}
            onChange={setSystem}
            label="System schemas"
          />
        </div>
        <div className="max-h-[65vh] overflow-y-auto py-1 text-xs">
          <TreeRow
            icon={DbIcon}
            label={db}
            active={sel.kind === "database"}
            onClick={() => setSel({ kind: "database" })}
          />
          {schema.error && (
            <div className="p-2">
              <Alert>{errText(schema.error)}</Alert>
            </div>
          )}
          {tree.map((s) => {
            const isOpen = open[s.name] || !!filter;
            return (
              <div key={s.name}>
                <TreeRow
                  depth={1}
                  icon={FolderTree}
                  label={s.name}
                  muted={s.system}
                  count={s.objects.length}
                  expanded={isOpen}
                  onToggle={() => setOpen((o) => ({ ...o, [s.name]: !isOpen }))}
                  active={sel.kind === "schema" && sel.schema === s.name}
                  onClick={() => {
                    setSel({ kind: "schema", schema: s.name });
                    setOpen((o) => ({ ...o, [s.name]: true }));
                  }}
                />
                {isOpen &&
                  kindOrder.map((k) => {
                    const objs = s.objects.filter((o) => o.kind === k);
                    if (!objs.length) return null;
                    const key = `${s.name}/${k}`;
                    const kOpen = open[key] ?? (k === "table" || !!filter);
                    return (
                      <div key={k}>
                        <TreeRow
                          depth={2}
                          label={kindLabel[k]}
                          muted
                          count={objs.length}
                          expanded={kOpen}
                          onToggle={() =>
                            setOpen((o) => ({ ...o, [key]: !kOpen }))
                          }
                          onClick={() =>
                            setOpen((o) => ({ ...o, [key]: !kOpen }))
                          }
                        />
                        {kOpen &&
                          objs.map((o) => (
                            <TreeRow
                              key={o.oid}
                              depth={3}
                              icon={kindIcon[o.kind]}
                              label={o.name}
                              mono
                              active={
                                sel.kind === "object" &&
                                sel.object.oid === o.oid
                              }
                              onClick={() =>
                                setSel({
                                  kind: "object",
                                  schema: s.name,
                                  object: o,
                                })
                              }
                            />
                          ))}
                      </div>
                    );
                  })}
              </div>
            );
          })}
        </div>
      </Panel>
      <div className="min-w-0">
        {sel.kind === "database" && <DatabaseView path={path} db={db} />}
        {sel.kind === "schema" && (
          <SchemaView
            path={path}
            db={db}
            schema={tree.find((s) => s.name === sel.schema)}
          />
        )}
        {sel.kind === "object" && (
          <ObjectView
            key={`${db}/${sel.object.oid}`}
            path={path}
            db={db}
            schema={sel.schema}
            object={sel.object}
          />
        )}
      </div>
    </div>
  );
}

function TreeRow({
  depth = 0,
  icon: Icon,
  label,
  count,
  expanded,
  onToggle,
  active,
  onClick,
  muted,
  mono,
}: {
  depth?: number;
  icon?: React.ComponentType<{ className?: string }>;
  label: string;
  count?: number;
  expanded?: boolean;
  onToggle?: () => void;
  active?: boolean;
  onClick: () => void;
  muted?: boolean;
  mono?: boolean;
}) {
  return (
    <div
      className={cn(
        "flex h-7 cursor-pointer items-center gap-1 pr-2",
        active ? "bg-raised text-fg" : "hover:bg-hover/60",
        muted && !active && "text-muted",
      )}
      style={{ paddingLeft: 6 + depth * 12 }}
      onClick={onClick}
    >
      {onToggle ? (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            onToggle();
          }}
          className="text-faint hover:text-fg"
          aria-label={expanded ? "Collapse" : "Expand"}
        >
          {expanded ? (
            <ChevronDown className="size-3.5" />
          ) : (
            <ChevronRight className="size-3.5" />
          )}
        </button>
      ) : (
        <span className="w-3.5" />
      )}
      {Icon && <Icon className="text-faint size-3.5 shrink-0" />}
      <span
        className={cn("min-w-0 flex-1 truncate", mono && "font-mono")}
        title={label}
      >
        {label}
      </span>
      {count !== undefined && <span className="text-faint">{count}</span>}
    </div>
  );
}

// ── database ────────────────────────────────────────────────────────────────

function DatabaseView({ path, db }: { path: string; db: string }) {
  const dbs = usePgDatabases(path);
  const d = dbs.data?.find((x) => x.name === db);
  const [tab, setTab] = useState<"extensions" | "privileges">("extensions");
  return (
    <div className={cn("flex flex-col", gap)}>
      <Panel title={`Database ${db}`}>
        {d ? (
          <div className="grid grid-cols-2 gap-3 text-xs sm:grid-cols-4">
            <Fact
              label="Owner"
              value={<span className="font-mono">{d.owner}</span>}
            />
            <Fact label="Size" value={bytes(d.sizeBytes)} />
            <Fact
              label="Connections"
              value={`${d.connections}${d.connLimit >= 0 ? ` / ${d.connLimit}` : ""}`}
            />
            <Fact label="Encoding" value={`${d.encoding}, ${d.collation}`} />
            {d.comment && <Fact label="Comment" value={d.comment} />}
          </div>
        ) : (
          <p className="text-faint text-xs">Loading…</p>
        )}
      </Panel>
      <Tabs
        tabs={["extensions", "privileges"] as const}
        value={tab}
        onChange={setTab}
      />
      {tab === "extensions" && <Extensions path={path} db={db} />}
      {tab === "privileges" && (
        <Panel title="Privileges on the database">
          <PrivilegesEditor
            path={path}
            db={db}
            object={{ kind: "database", name: db }}
          />
        </Panel>
      )}
    </div>
  );
}

function Extensions({ path, db }: { path: string; db: string }) {
  const qc = useQueryClient();
  const exts = usePgExtensions(path, db);
  const change = useMutation({
    mutationFn: (b: { name: string; action: string; cascade?: boolean }) =>
      api("POST", pgUrl(`${path}/pg/extensions`, db), b),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["pg", path, "extensions", db] });
      void qc.invalidateQueries({ queryKey: ["pg", path, "schema", db] });
    },
  });
  return (
    <Panel title="Extensions" flush>
      {change.error && (
        <div className="p-2">
          <Alert>{errText(change.error)}</Alert>
        </div>
      )}
      {exts.error && (
        <div className="p-2">
          <Alert>{errText(exts.error)}</Alert>
        </div>
      )}
      <DataTable
        rows={exts.data ?? []}
        rowKey={(e) => e.name}
        columns={[
          {
            header: "Extension",
            cell: (e) => <span className="font-mono">{e.name}</span>,
          },
          {
            header: "Installed",
            cell: (e) =>
              e.installedVersion ? (
                <span>
                  {e.installedVersion}
                  {e.schema && (
                    <span className="text-faint"> in {e.schema}</span>
                  )}
                  {e.installedVersion !== e.defaultVersion && (
                    <span className="text-warn"> → {e.defaultVersion}</span>
                  )}
                </span>
              ) : (
                <span className="text-faint">—</span>
              ),
          },
          {
            header: "Description",
            className: "whitespace-normal min-w-64",
            cell: (e) => (
              <span className="text-muted">
                {e.comment}
                {e.preloaded && (
                  <span className="text-faint"> · preloaded</span>
                )}
              </span>
            ),
          },
          {
            header: "",
            cell: (e) =>
              e.name === "plpgsql" ? null : e.installedVersion ? (
                <span className="flex gap-1">
                  {e.installedVersion !== e.defaultVersion && (
                    <Button
                      variant="ghost"
                      disabled={change.isPending}
                      onClick={() =>
                        change.mutate({ name: e.name, action: "update" })
                      }
                    >
                      Update
                    </Button>
                  )}
                  <Button
                    variant="ghost"
                    disabled={change.isPending}
                    onClick={async () => {
                      if (
                        await confirmAction(
                          `Drop the extension ${e.name} from ${db}? Objects that use it must be dropped first.`,
                        )
                      )
                        change.mutate({ name: e.name, action: "drop" });
                    }}
                  >
                    Drop
                  </Button>
                </span>
              ) : (
                <Button
                  disabled={change.isPending}
                  onClick={() =>
                    change.mutate({
                      name: e.name,
                      action: "install",
                      cascade: true,
                    })
                  }
                >
                  <Plus className="size-3.5" /> Install
                </Button>
              ),
          },
        ]}
      />
    </Panel>
  );
}

// ── schema ──────────────────────────────────────────────────────────────────

function SchemaView({
  path,
  db,
  schema,
}: {
  path: string;
  db: string;
  schema?: {
    name: string;
    owner: string;
    comment?: string;
    objects: PgObject[];
  };
}) {
  if (!schema) return null;
  const tables = schema.objects.filter((o) => browsable(o.kind));
  return (
    <div className={cn("flex flex-col", gap)}>
      <Panel title={`Schema ${schema.name}`}>
        <div className="grid grid-cols-2 gap-3 text-xs sm:grid-cols-4">
          <Fact
            label="Owner"
            value={<span className="font-mono">{schema.owner}</span>}
          />
          <Fact label="Objects" value={schema.objects.length} />
          <Fact label="Tables and views" value={tables.length} />
          <Fact
            label="Size"
            value={bytes(tables.reduce((n, o) => n + (o.sizeBytes ?? 0), 0))}
          />
          {schema.comment && <Fact label="Comment" value={schema.comment} />}
        </div>
      </Panel>
      {tables.length > 0 && (
        <Panel title="Tables" flush>
          <DataTable
            rows={tables}
            rowKey={(o) => String(o.oid)}
            columns={[
              {
                header: "Name",
                cell: (o) => <span className="font-mono">{o.name}</span>,
              },
              { header: "Kind", cell: (o) => o.kind },
              {
                header: "Rows (est.)",
                cell: (o) => (o.rowEstimate ?? 0).toLocaleString(),
              },
              {
                header: "Size",
                cell: (o) => (o.sizeBytes ? bytes(o.sizeBytes) : "—"),
              },
              {
                header: "Owner",
                cell: (o) => <span className="font-mono">{o.owner}</span>,
              },
            ]}
          />
        </Panel>
      )}
      <Panel title="Privileges on the schema">
        <PrivilegesEditor
          path={path}
          db={db}
          object={{ kind: "schema", name: schema.name }}
        />
      </Panel>
    </div>
  );
}

// ── object ──────────────────────────────────────────────────────────────────

type ObjectTab =
  | "columns"
  | "data"
  | "indexes"
  | "constraints"
  | "triggers"
  | "privileges"
  | "ddl";

function ObjectView({
  path,
  db,
  schema,
  object,
}: {
  path: string;
  db: string;
  schema: string;
  object: PgObject;
}) {
  const routine = ["function", "procedure", "aggregate"].includes(object.kind);
  const q = usePgObject(
    path,
    db,
    schema,
    object.name,
    routine ? object.oid : undefined,
  );
  const o = q.data;
  const rel = !routine && object.kind !== "type";
  const tabs: ObjectTab[] = rel
    ? [
        "columns",
        ...(browsable(object.kind) ? (["data"] as ObjectTab[]) : []),
        ...(object.kind !== "sequence" && object.kind !== "view"
          ? (["indexes", "constraints", "triggers"] as ObjectTab[])
          : []),
        "privileges",
        "ddl",
      ]
    : object.kind === "type"
      ? ["ddl"]
      : ["ddl", "privileges"];
  const [tab, setTab] = useState<ObjectTab>(tabs[0] ?? "ddl");
  const privRef: PgObjectRef | null =
    object.kind === "sequence"
      ? { kind: "sequence", schema, name: object.name }
      : routine
        ? { kind: "function", oid: object.oid }
        : rel
          ? { kind: "table", schema, name: object.name }
          : null;
  const Icon = kindIcon[object.kind];
  return (
    <div className={cn("flex flex-col", gap)}>
      <Panel
        title={
          <span className="flex items-center gap-1.5 normal-case">
            <Icon className="size-3.5" />
            <span className="font-mono tracking-normal">
              {schema}.{object.name}
            </span>
          </span>
        }
      >
        {q.error && <Alert>{errText(q.error)}</Alert>}
        {o && (
          <div className="grid grid-cols-2 gap-3 text-xs sm:grid-cols-4 xl:grid-cols-6">
            <Fact label="Kind" value={object.kind} />
            <Fact
              label="Owner"
              value={<span className="font-mono">{o.owner}</span>}
            />
            {rel && (
              <Fact
                label="Rows (est.)"
                value={(o.rowEstimate ?? 0).toLocaleString()}
              />
            )}
            {rel && !!o.sizeBytes && (
              <Fact
                label="Size"
                value={
                  <span
                    title={`table ${bytes(o.tableBytes ?? 0)}, indexes ${bytes(o.indexBytes ?? 0)}, TOAST ${bytes(o.toastBytes ?? 0)}`}
                  >
                    {bytes(o.sizeBytes)}
                  </span>
                }
              />
            )}
            {rel && object.kind !== "view" && (
              <Fact
                label="Dead rows"
                value={(o.deadRows ?? 0).toLocaleString()}
              />
            )}
            {rel && object.kind !== "view" && (
              <Fact
                label="Scans"
                value={`${o.seqScans ?? 0} seq · ${o.indexScans ?? 0} index`}
              />
            )}
            {rel && object.kind !== "view" && (
              <Fact
                label="Vacuumed"
                value={ago(o.lastAutovacuum ?? o.lastVacuum)}
              />
            )}
            {rel && object.kind !== "view" && (
              <Fact label="Analyzed" value={ago(o.lastAnalyze)} />
            )}
            {o.partitionOf && (
              <Fact label="Partition of" value={o.partitionOf} />
            )}
            {o.partitionKey && (
              <Fact label="Partitioned by" value={o.partitionKey} />
            )}
            {o.comment && <Fact label="Comment" value={o.comment} />}
          </div>
        )}
      </Panel>
      <Tabs
        tabs={tabs}
        value={tab}
        onChange={setTab}
        label={(t) => (t === "ddl" ? "DDL" : t)}
      />
      {o && tab === "columns" && (
        <Panel flush>
          <DataTable
            rows={o.columns}
            rowKey={(c) => c.name}
            columns={[
              {
                header: "Column",
                cell: (c) => <span className="font-mono">{c.name}</span>,
              },
              {
                header: "Type",
                cell: (c) => <span className="font-mono">{c.type}</span>,
              },
              {
                header: "Null",
                cell: (c) =>
                  c.notNull ? (
                    "not null"
                  ) : (
                    <span className="text-faint">null</span>
                  ),
              },
              {
                header: "Default",
                className: "whitespace-normal",
                cell: (c) => (
                  <span className="font-mono">
                    {c.identity
                      ? `identity (${c.identity})`
                      : c.generated
                        ? `generated: ${c.generated}`
                        : (c.default ?? "")}
                  </span>
                ),
              },
              {
                header: "Keys",
                cell: (c) =>
                  o.constraints
                    .filter(
                      (k) =>
                        ["primary key", "unique", "foreign key"].includes(
                          k.kind,
                        ) && keyColumns(k.definition).includes(c.name),
                    )
                    .map((k) =>
                      k.kind === "primary key"
                        ? "PK"
                        : k.kind === "unique"
                          ? "UQ"
                          : `FK → ${k.references}`,
                    )
                    .join(", "),
              },
              {
                header: "Comment",
                className: "whitespace-normal",
                cell: (c) => <span className="text-muted">{c.comment}</span>,
              },
            ]}
          />
        </Panel>
      )}
      {tab === "data" && (
        <DataView path={path} db={db} schema={schema} table={object.name} />
      )}
      {o && tab === "indexes" && (
        <Panel flush>
          <DataTable
            rows={o.indexes}
            rowKey={(i) => i.name}
            empty={<EmptyState icon={Search} title="No indexes" />}
            columns={[
              {
                header: "Index",
                cell: (i) => (
                  <span className="font-mono">
                    {i.name}
                    {!i.valid && <span className="text-bad"> invalid</span>}
                  </span>
                ),
              },
              {
                header: "Definition",
                className: "whitespace-normal",
                cell: (i) => (
                  <span className="font-mono">
                    {i.definition.replace(
                      /^CREATE (UNIQUE )?INDEX \S+ ON /,
                      "",
                    )}
                  </span>
                ),
              },
              { header: "Size", cell: (i) => bytes(i.sizeBytes) },
              { header: "Scans", cell: (i) => i.scans.toLocaleString() },
            ]}
          />
        </Panel>
      )}
      {o && tab === "constraints" && (
        <Panel flush>
          <DataTable
            rows={o.constraints}
            rowKey={(k) => k.name}
            empty={<EmptyState icon={Search} title="No constraints" />}
            columns={[
              {
                header: "Constraint",
                cell: (k) => <span className="font-mono">{k.name}</span>,
              },
              { header: "Kind", cell: (k) => k.kind },
              {
                header: "Definition",
                className: "whitespace-normal",
                cell: (k) => <span className="font-mono">{k.definition}</span>,
              },
            ]}
          />
        </Panel>
      )}
      {o && tab === "triggers" && (
        <Panel flush>
          <DataTable
            rows={o.triggers}
            rowKey={(t) => t.name}
            empty={<EmptyState icon={Search} title="No triggers" />}
            columns={[
              {
                header: "Trigger",
                cell: (t) => <span className="font-mono">{t.name}</span>,
              },
              { header: "Enabled", cell: (t) => (t.enabled ? "yes" : "no") },
              {
                header: "Definition",
                className: "whitespace-normal",
                cell: (t) => <span className="font-mono">{t.definition}</span>,
              },
            ]}
          />
        </Panel>
      )}
      {o && tab === "ddl" && (
        <Panel>
          <SqlBlock sql={o.ddl} />
          {o.partitionList && o.partitionList.length > 0 && (
            <div className="mt-2 text-xs">
              <h3 className="text-muted mb-1 font-medium">Partitions</h3>
              <SqlBlock sql={o.partitionList.join("\n")} />
            </div>
          )}
        </Panel>
      )}
      {tab === "privileges" && privRef && (
        <Panel>
          <PrivilegesEditor path={path} db={db} object={privRef} />
        </Panel>
      )}
    </div>
  );
}

/** The columns of PRIMARY KEY (a, b), UNIQUE (…) or FOREIGN KEY (…) REFERENCES …. */
function keyColumns(def: string) {
  const m = /\(([^)]*)\)/.exec(def);
  return (m?.[1] ?? "").split(",").map((c) => c.trim().replace(/^"|"$/g, ""));
}

const OPS: [string, string][] = [
  ["=", "="],
  ["<>", "≠"],
  ["<", "<"],
  ["<=", "≤"],
  [">", ">"],
  [">=", "≥"],
  ["like", "like"],
  ["ilike", "ilike"],
  ["null", "is null"],
  ["notnull", "is not null"],
];

const PAGE = 100;

/** A page of a table's rows, sortable and filterable, read-only. */
function DataView({
  path,
  db,
  schema,
  table,
}: {
  path: string;
  db: string;
  schema: string;
  table: string;
}) {
  const [offset, setOffset] = useState(0);
  const [sort, setSort] = useState<
    { column: string; desc: boolean } | undefined
  >();
  const [filters, setFilters] = useState<PgRowFilter[]>([]);
  const [draft, setDraft] = useState<PgRowFilter>({
    column: "",
    op: "=",
    value: "",
  });
  const [count, setCount] = useState(false);
  const q = useQuery({
    queryKey: [
      "pg",
      path,
      "rows",
      db,
      schema,
      table,
      offset,
      sort,
      filters,
      count,
    ],
    queryFn: () =>
      api<PgRows>("POST", pgUrl(`${path}/pg/rows`, db), {
        schema,
        table,
        limit: PAGE,
        offset,
        orderBy: sort?.column ?? "",
        desc: sort?.desc ?? false,
        filters,
        count,
      }),
    placeholderData: (prev) => prev,
  });
  const cols = q.data?.columns ?? [];
  const addFilter = () => {
    const f = { ...draft, column: draft.column || cols[0]?.name || "" };
    if (!f.column) return;
    setFilters((x) => [...x, f]);
    setDraft({ column: f.column, op: "=", value: "" });
    setOffset(0);
  };
  return (
    <Panel
      flush
      title={
        q.data
          ? q.data.total !== undefined
            ? `${q.data.total.toLocaleString()} rows`
            : `rows ${offset + 1}–${offset + q.data.rows.length}${q.data.more ? "+" : ""}`
          : "rows"
      }
      actions={
        <>
          <Button
            variant="ghost"
            onClick={() => setCount(true)}
            disabled={count}
          >
            Count
          </Button>
          <IconButton
            label="Previous page"
            disabled={offset === 0}
            onClick={() => setOffset((o) => Math.max(0, o - PAGE))}
          >
            <ChevronLeft className="size-3.5" />
          </IconButton>
          <IconButton
            label="Next page"
            disabled={!q.data?.more}
            onClick={() => setOffset((o) => o + PAGE)}
          >
            <ChevronRight className="size-3.5" />
          </IconButton>
          <IconButton label="Reload" onClick={() => void q.refetch()}>
            <RefreshCw
              className={cn("size-3.5", q.isFetching && "animate-spin")}
            />
          </IconButton>
        </>
      }
    >
      <div className="border-line flex flex-wrap items-center gap-1.5 border-b p-2 text-xs">
        {filters.map((f, i) => (
          <span
            key={i}
            className="bg-raised border-line flex items-center gap-1 rounded-sm border px-1.5 py-0.5 font-mono"
          >
            {f.column} {OPS.find((o) => o[0] === f.op)?.[1]}{" "}
            {f.op !== "null" && f.op !== "notnull"
              ? JSON.stringify(f.value)
              : ""}
            <button
              type="button"
              aria-label="Remove filter"
              onClick={() => (
                setFilters((x) => x.filter((_, j) => j !== i)),
                setOffset(0)
              )}
            >
              <X className="text-faint hover:text-fg size-3" />
            </button>
          </span>
        ))}
        <select
          className={cn(inlineSelect, "w-36")}
          value={draft.column}
          onChange={(e) => setDraft({ ...draft, column: e.target.value })}
          aria-label="Filter column"
        >
          {cols.map((c) => (
            <option key={c.name}>{c.name}</option>
          ))}
        </select>
        <select
          className={cn(inlineSelect, "w-24")}
          value={draft.op}
          onChange={(e) => setDraft({ ...draft, op: e.target.value })}
          aria-label="Filter operator"
        >
          {OPS.map(([v, l]) => (
            <option key={v} value={v}>
              {l}
            </option>
          ))}
        </select>
        {draft.op !== "null" && draft.op !== "notnull" && (
          <Input
            value={draft.value}
            onChange={(e) => setDraft({ ...draft, value: e.target.value })}
            onKeyDown={(e) => e.key === "Enter" && addFilter()}
            placeholder={draft.op.includes("like") ? "%text%" : "value"}
            className="h-7 w-40 font-mono"
            aria-label="Filter value"
          />
        )}
        <Button onClick={addFilter}>Filter</Button>
      </div>
      {q.error && (
        <div className="p-2">
          <Alert>{errText(q.error)}</Alert>
        </div>
      )}
      {q.data &&
        (q.data.rows.length ? (
          <ResultGrid
            columns={q.data.columns}
            rows={q.data.rows}
            offset={offset}
            sort={sort}
            onSort={(c) => {
              setSort((s) =>
                s?.column === c
                  ? s.desc
                    ? undefined
                    : { column: c, desc: true }
                  : { column: c, desc: false },
              );
              setOffset(0);
            }}
          />
        ) : (
          <EmptyState
            icon={Search}
            title={filters.length ? "No rows match" : "No rows"}
          />
        ))}
    </Panel>
  );
}
