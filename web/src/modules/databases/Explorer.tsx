import {
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
} from "react";
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { KeyRound, Plus, RefreshCw, Search, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { bytes } from "@/lib/nodes";
import { Panel } from "@/ui/Panel";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, IconButton, Input } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { selectClass } from "./NewDatabaseWizard";

interface KeyInfo {
  key: string;
  type: string;
  ttlMs: number;
  bytes: number;
}

interface KeyPage {
  cursor: number;
  total: number;
  keys: KeyInfo[];
}

interface Value {
  key: string;
  type: string;
  ttlMs: number;
  bytes: number;
  length: number;
  truncated: boolean;
  string?: string;
  hash?: Record<string, string>;
  list?: string[];
  set?: string[];
  zset?: { member: string; score: number }[];
  stream?: { id: string; fields: Record<string, string> }[];
}

const TYPES = ["string", "hash", "list", "set", "zset", "stream"] as const;

const typeColor: Record<string, string> = {
  string: "text-[#3987e5] border-[#3987e5]/40",
  hash: "text-[#d95926] border-[#d95926]/40",
  list: "text-[#199e70] border-[#199e70]/40",
  set: "text-[#c98500] border-[#c98500]/40",
  zset: "text-[#d55181] border-[#d55181]/40",
  stream: "text-[#9085e9] border-[#9085e9]/40",
};

const errText = (e: unknown) =>
  e instanceof ApiError ? e.message : "Request failed";

export function fmtTTL(ms: number) {
  if (ms < 0) return "no expiry";
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  if (s < 86400)
    return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

function TypeBadge({ type }: { type: string }) {
  return (
    <span
      className={cn(
        "inline-flex w-12 shrink-0 justify-center rounded-sm border px-1 font-mono text-[10px] uppercase",
        typeColor[type] ?? "text-muted border-line",
      )}
    >
      {type}
    </span>
  );
}

/** Key browser: SCAN by pattern and type, a typed value viewer and editor. */
export function Explorer({ path }: { path: string }) {
  const [pattern, setPattern] = useState("*");
  const [applied, setApplied] = useState("*");
  const [type, setType] = useState("");
  const [selected, setSelected] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);
  const keys = useInfiniteQuery({
    queryKey: ["db-keys", path, applied, type],
    queryFn: ({ pageParam }) =>
      api<KeyPage>(
        "GET",
        `${path}/keys?${new URLSearchParams({ pattern: applied, type, cursor: String(pageParam), count: "200" })}`,
      ),
    initialPageParam: 0,
    getNextPageParam: (last) => (last.cursor === 0 ? undefined : last.cursor),
    retry: false,
  });
  const list = keys.data?.pages.flatMap((p) => p.keys) ?? [];
  const total = keys.data?.pages[0]?.total ?? 0;
  const search = (e: FormEvent) => {
    e.preventDefault();
    setApplied(pattern.trim() || "*");
  };
  return (
    <div
      className={cn(
        "grid grid-cols-1 items-start lg:grid-cols-[minmax(18rem,26rem)_1fr]",
        gap,
      )}
    >
      <Panel
        title={`Keys (${total.toLocaleString()} in the database)`}
        flush
        actions={
          <>
            <IconButton label="Refresh" onClick={() => void keys.refetch()}>
              <RefreshCw className="size-3.5" />
            </IconButton>
            <IconButton
              label="New key"
              onClick={() => {
                setAdding(true);
                setSelected(null);
              }}
            >
              <Plus className="size-3.5" />
            </IconButton>
          </>
        }
      >
        <form
          onSubmit={search}
          className="border-line flex gap-1.5 border-b p-1.5"
        >
          <div className="relative min-w-0 flex-1">
            <Search className="text-faint pointer-events-none absolute top-2 left-2 size-3.5" />
            <Input
              value={pattern}
              onChange={(e) => setPattern(e.target.value)}
              placeholder="user:*"
              className="h-7 w-full pl-7 font-mono"
              aria-label="Key pattern"
            />
          </div>
          <select
            className="bg-bg border-line-strong focus:border-accent h-7 w-28 shrink-0 rounded-sm border px-1.5 text-xs outline-none"
            value={type}
            onChange={(e) => setType(e.target.value)}
            aria-label="Key type"
          >
            <option value="">all types</option>
            {TYPES.map((t) => (
              <option key={t}>{t}</option>
            ))}
          </select>
        </form>
        {keys.error && (
          <div className="p-1.5">
            <Alert tone="warn">{errText(keys.error)}</Alert>
          </div>
        )}
        <ul className="divide-line max-h-[60vh] divide-y overflow-y-auto">
          {list.map((k) => (
            <li key={k.key}>
              <button
                onClick={() => {
                  setSelected(k.key);
                  setAdding(false);
                }}
                className={cn(
                  "hover:bg-hover/50 flex w-full items-center gap-2 px-2 py-1 text-left text-xs",
                  selected === k.key && "bg-raised",
                )}
              >
                <TypeBadge type={k.type} />
                <span
                  className="min-w-0 flex-1 truncate font-mono"
                  title={k.key}
                >
                  {k.key}
                </span>
                <span className="text-faint shrink-0 tabular-nums">
                  {k.ttlMs >= 0 ? fmtTTL(k.ttlMs) : ""} {bytes(k.bytes)}
                </span>
              </button>
            </li>
          ))}
        </ul>
        {list.length === 0 && !keys.isLoading && !keys.error && (
          <p className="text-faint p-3 text-xs">No keys match {applied}.</p>
        )}
        {keys.hasNextPage && (
          <div className="border-line border-t p-1.5">
            <Button
              variant="ghost"
              disabled={keys.isFetchingNextPage}
              onClick={() => void keys.fetchNextPage()}
            >
              Load more
            </Button>
          </div>
        )}
      </Panel>
      {adding ? (
        <NewKey
          path={path}
          onDone={(k) => {
            setAdding(false);
            setSelected(k);
            void keys.refetch();
          }}
        />
      ) : selected ? (
        <KeyView
          path={path}
          keyName={selected}
          onDeleted={() => {
            setSelected(null);
            void keys.refetch();
          }}
        />
      ) : (
        <Panel>
          <EmptyState icon={KeyRound} title="Pick a key">
            Search with a pattern such as{" "}
            <code className="font-mono">session:*</code>, filter by type, or add
            a key.
          </EmptyState>
        </Panel>
      )}
    </div>
  );
}

function KeyView({
  path,
  keyName,
  onDeleted,
}: {
  path: string;
  keyName: string;
  onDeleted: () => void;
}) {
  const qc = useQueryClient();
  const q = `key=${encodeURIComponent(keyName)}`;
  const value = useQuery({
    queryKey: ["db-key", path, keyName],
    queryFn: () => api<Value>("GET", `${path}/key?${q}`),
    retry: false,
  });
  const refresh = (v?: Value) => {
    if (v) qc.setQueryData(["db-key", path, keyName], v);
    else void value.refetch();
  };
  const del = useMutation({
    mutationFn: () => api("DELETE", `${path}/key?${q}`),
    onSuccess: onDeleted,
  });
  const [ttl, setTtl] = useState("");
  const expire = useMutation({
    mutationFn: (seconds: number) =>
      api<Value>("PUT", `${path}/key/ttl?${q}`, { ttlSeconds: seconds }),
    onSuccess: (v) => {
      setTtl("");
      refresh(v);
    },
  });
  const run = useMutation({
    mutationFn: (args: string[]) => api("POST", `${path}/command`, { args }),
    onSuccess: () => refresh(),
  });
  const v = value.data;
  return (
    <Panel
      title={<span className="font-mono normal-case">{keyName}</span>}
      actions={
        <>
          <IconButton label="Reload" onClick={() => refresh()}>
            <RefreshCw className="size-3.5" />
          </IconButton>
          <IconButton
            label="Delete key"
            onClick={() => confirm(`Delete ${keyName}?`) && del.mutate()}
          >
            <Trash2 className="size-3.5" />
          </IconButton>
        </>
      }
    >
      {value.error && <Alert tone="warn">{errText(value.error)}</Alert>}
      {v && (
        <div className="flex flex-col gap-3 text-xs">
          <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
            <TypeBadge type={v.type} />
            <span className="text-muted">
              {v.type === "string"
                ? `${v.length.toLocaleString()} bytes`
                : `${v.length.toLocaleString()} items`}{" "}
              · {bytes(v.bytes)} in memory
            </span>
            <span className="text-muted">TTL: {fmtTTL(v.ttlMs)}</span>
            <form
              className="flex items-center gap-1"
              onSubmit={(e) => {
                e.preventDefault();
                if (ttl) expire.mutate(Number(ttl));
              }}
            >
              <Input
                value={ttl}
                onChange={(e) => setTtl(e.target.value)}
                placeholder="seconds"
                className="h-6 w-20"
                aria-label="TTL in seconds"
              />
              <Button type="submit" variant="ghost" disabled={!ttl}>
                Set TTL
              </Button>
              {v.ttlMs >= 0 && (
                <Button variant="ghost" onClick={() => expire.mutate(-1)}>
                  Persist
                </Button>
              )}
            </form>
          </div>
          {(expire.error || run.error || del.error) && (
            <Alert>{errText(expire.error ?? run.error ?? del.error)}</Alert>
          )}
          {v.truncated && (
            <Alert tone="info">
              Showing the first {v.type === "stream" ? 100 : 500} items; use the
              console for the rest.
            </Alert>
          )}
          <ValueEditor
            path={path}
            v={v}
            onSaved={refresh}
            run={(args) => run.mutate(args)}
          />
        </div>
      )}
    </Panel>
  );
}

function ValueEditor({
  path,
  v,
  onSaved,
  run,
}: {
  path: string;
  v: Value;
  onSaved: (v: Value) => void;
  run: (args: string[]) => void;
}) {
  const [text, setText] = useState(v.string ?? "");
  const [a, setA] = useState("");
  const [b, setB] = useState("");
  useEffect(() => setText(v.string ?? ""), [v.string]);
  const write = useMutation({
    mutationFn: (body: object) =>
      api<Value>("PUT", `${path}/key?key=${encodeURIComponent(v.key)}`, body),
    onSuccess: (nv) => {
      setA("");
      setB("");
      onSaved(nv);
    },
  });
  const cell = "border-line border-b px-2 py-1 align-top";
  const del = (args: string[]) =>
    confirm(`Run ${args.join(" ")}?`) && run(args);
  const addRow = (label: [string, string?], onAdd: () => void) => (
    <form
      className="flex flex-wrap items-end gap-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        onAdd();
      }}
    >
      <Field label={label[0]}>
        <Input
          value={a}
          onChange={(e) => setA(e.target.value)}
          className="h-7 w-48 font-mono"
        />
      </Field>
      {label[1] && (
        <Field label={label[1]}>
          <Input
            value={b}
            onChange={(e) => setB(e.target.value)}
            className="h-7 w-48 font-mono"
          />
        </Field>
      )}
      <Button type="submit" disabled={!a || write.isPending}>
        <Plus className="size-3.5" /> Add
      </Button>
    </form>
  );
  return (
    <>
      {write.error && <Alert>{errText(write.error)}</Alert>}
      {v.type === "string" && (
        <div className="flex flex-col gap-1.5">
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            spellCheck={false}
            className="bg-bg border-line-strong focus:border-accent min-h-48 w-full rounded-sm border p-2 font-mono text-xs outline-none"
          />
          <div>
            <Button
              variant="primary"
              disabled={text === v.string || write.isPending || v.truncated}
              onClick={() =>
                write.mutate({
                  type: "string",
                  string: text,
                  ttlSeconds: v.ttlMs > 0 ? Math.ceil(v.ttlMs / 1000) : 0,
                })
              }
            >
              Save
            </Button>
          </div>
        </div>
      )}
      {v.type === "hash" && (
        <>
          <Table head={["Field", "Value", ""]}>
            {Object.entries(v.hash ?? {}).map(([k, val]) => (
              <tr key={k}>
                <td className={cn(cell, "font-mono")}>{k}</td>
                <td className={cn(cell, "font-mono break-all")}>{val}</td>
                <td className={cell}>
                  <IconButton
                    label="Delete field"
                    onClick={() => del(["HDEL", v.key, k])}
                  >
                    <Trash2 className="size-3" />
                  </IconButton>
                </td>
              </tr>
            ))}
          </Table>
          {addRow(["Field", "Value"], () =>
            write.mutate({ type: "hash", hash: { [a]: b } }),
          )}
        </>
      )}
      {v.type === "list" && (
        <>
          <Table head={["#", "Item", ""]}>
            {(v.list ?? []).map((x, i) => (
              <tr key={i}>
                <td className={cn(cell, "text-faint w-10 tabular-nums")}>
                  {i}
                </td>
                <td className={cn(cell, "font-mono break-all")}>{x}</td>
                <td className={cell}>
                  <IconButton
                    label="Remove item"
                    onClick={() => del(["LREM", v.key, "1", x])}
                  >
                    <Trash2 className="size-3" />
                  </IconButton>
                </td>
              </tr>
            ))}
          </Table>
          {addRow(["Append item"], () =>
            write.mutate({ type: "list", list: [a] }),
          )}
        </>
      )}
      {v.type === "set" && (
        <>
          <Table head={["Member", ""]}>
            {(v.set ?? []).map((x) => (
              <tr key={x}>
                <td className={cn(cell, "font-mono break-all")}>{x}</td>
                <td className={cell}>
                  <IconButton
                    label="Remove member"
                    onClick={() => del(["SREM", v.key, x])}
                  >
                    <Trash2 className="size-3" />
                  </IconButton>
                </td>
              </tr>
            ))}
          </Table>
          {addRow(["Member"], () => write.mutate({ type: "set", set: [a] }))}
        </>
      )}
      {v.type === "zset" && (
        <>
          <Table head={["Score", "Member", ""]}>
            {(v.zset ?? []).map((z) => (
              <tr key={z.member}>
                <td className={cn(cell, "w-24 tabular-nums")}>{z.score}</td>
                <td className={cn(cell, "font-mono break-all")}>{z.member}</td>
                <td className={cell}>
                  <IconButton
                    label="Remove member"
                    onClick={() => del(["ZREM", v.key, z.member])}
                  >
                    <Trash2 className="size-3" />
                  </IconButton>
                </td>
              </tr>
            ))}
          </Table>
          {addRow(["Member", "Score"], () =>
            write.mutate({
              type: "zset",
              zset: [{ member: a, score: Number(b) || 0 }],
            }),
          )}
        </>
      )}
      {v.type === "stream" && (
        <Table head={["ID", "Fields"]}>
          {(v.stream ?? []).map((e) => (
            <tr key={e.id}>
              <td className={cn(cell, "w-44 font-mono")}>{e.id}</td>
              <td className={cn(cell, "font-mono break-all")}>
                {Object.entries(e.fields)
                  .map(([k, x]) => `${k}=${x}`)
                  .join("  ")}
              </td>
            </tr>
          ))}
        </Table>
      )}
    </>
  );
}

function Table({
  head,
  children,
}: {
  head: string[];
  children: React.ReactNode;
}) {
  return (
    <div className="border-line max-h-[50vh] overflow-auto rounded-sm border">
      <table className="w-full border-collapse text-xs">
        <thead className="bg-surface sticky top-0">
          <tr>
            {head.map((h, i) => (
              <th
                key={i}
                className="text-muted border-line border-b px-2 py-1 text-left font-medium"
              >
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

function NewKey({
  path,
  onDone,
}: {
  path: string;
  onDone: (key: string) => void;
}) {
  const [key, setKey] = useState("");
  const [type, setType] = useState<"string" | "hash" | "list" | "set" | "zset">(
    "string",
  );
  const [value, setValue] = useState("");
  const [ttl, setTtl] = useState("");
  const create = useMutation({
    mutationFn: () => {
      const lines = value.split("\n").filter((l) => l.trim() !== "");
      const body: Record<string, unknown> = {
        type,
        ttlSeconds: Number(ttl) || 0,
        replace: true,
      };
      if (type === "string") body.string = value;
      if (type === "hash")
        body.hash = Object.fromEntries(
          lines.map((l) => {
            const i = l.indexOf("=");
            return i < 0 ? [l, ""] : [l.slice(0, i), l.slice(i + 1)];
          }),
        );
      if (type === "list" || type === "set") body[type] = lines;
      if (type === "zset")
        body.zset = lines.map((l) => {
          const [score, ...m] = l.split(" ");
          return { score: Number(score) || 0, member: m.join(" ") };
        });
      return api("PUT", `${path}/key?key=${encodeURIComponent(key)}`, body);
    },
    onSuccess: () => onDone(key),
  });
  const help = {
    string: "The value.",
    hash: "One field=value per line.",
    list: "One item per line, in order.",
    set: "One member per line.",
    zset: "One “score member” per line, e.g. 10 alice.",
  }[type];
  return (
    <Panel title="New key">
      <form
        className="flex flex-col gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-[1fr_8rem_8rem]">
          <Field label="Key">
            <Input
              value={key}
              onChange={(e) => setKey(e.target.value)}
              className="font-mono"
              placeholder="user:42"
            />
          </Field>
          <Field label="Type">
            <select
              className={selectClass}
              value={type}
              onChange={(e) => setType(e.target.value as typeof type)}
            >
              {TYPES.filter((t) => t !== "stream").map((t) => (
                <option key={t}>{t}</option>
              ))}
            </select>
          </Field>
          <Field label="TTL (seconds)">
            <Input
              value={ttl}
              onChange={(e) => setTtl(e.target.value)}
              placeholder="none"
            />
          </Field>
        </div>
        <Field label="Value" hint={help}>
          <textarea
            value={value}
            onChange={(e) => setValue(e.target.value)}
            spellCheck={false}
            className="bg-bg border-line-strong focus:border-accent min-h-32 w-full rounded-sm border p-2 font-mono text-xs outline-none"
          />
        </Field>
        {create.error && <Alert>{errText(create.error)}</Alert>}
        <div>
          <Button
            type="submit"
            variant="primary"
            disabled={!key || create.isPending}
          >
            Create key
          </Button>
        </div>
      </form>
    </Panel>
  );
}

/** Splits a console line into arguments, honouring "double" and 'single' quotes. */
export function splitArgs(line: string): string[] {
  const out: string[] = [];
  let cur = "";
  let quote: string | null = null;
  let has = false;
  for (let i = 0; i < line.length; i++) {
    const c = line[i]!;
    if (quote) {
      if (c === "\\" && quote === '"' && i + 1 < line.length) {
        cur += line[++i];
      } else if (c === quote) {
        quote = null;
      } else {
        cur += c;
      }
    } else if (c === '"' || c === "'") {
      quote = c;
      has = true;
    } else if (/\s/.test(c)) {
      if (has || cur) out.push(cur);
      cur = "";
      has = false;
    } else {
      cur += c;
    }
  }
  if (has || cur) out.push(cur);
  return out;
}

/** Renders a reply like valkey-cli. */
function render(v: unknown, indent = ""): string {
  if (v === null || v === undefined) return indent + "(nil)";
  if (Array.isArray(v)) {
    if (v.length === 0) return indent + "(empty array)";
    return v
      .map((x, i) =>
        Array.isArray(x)
          ? `${indent}${i + 1})\n${render(x, indent + "   ")}`
          : `${indent}${i + 1}) ${render(x).trimStart()}`,
      )
      .join("\n");
  }
  if (typeof v === "number") return `${indent}(integer) ${v}`;
  if (typeof v === "string") return `${indent}"${v}"`;
  return indent + JSON.stringify(v);
}

interface Entry {
  cmd: string;
  out: string;
  error?: boolean;
}

/** A command console on the primary (administration commands are refused). */
export function Console({ path }: { path: string }) {
  const [line, setLine] = useState("");
  const [log, setLog] = useState<Entry[]>([]);
  const [hist, setHist] = useState<string[]>([]);
  const [hi, setHi] = useState(-1);
  const end = useRef<HTMLDivElement>(null);
  const run = useMutation({
    mutationFn: (args: string[]) =>
      api<{ result: unknown }>("POST", `${path}/command`, { args }),
  });
  useEffect(() => {
    // A block body: newer browsers return a Promise from scrollIntoView,
    // which React would take for a cleanup function.
    end.current?.scrollIntoView({ block: "nearest" });
  }, [log]);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const cmd = line.trim();
    if (!cmd) return;
    setHist((h) => [cmd, ...h.filter((x) => x !== cmd)].slice(0, 100));
    setHi(-1);
    setLine("");
    if (cmd.toLowerCase() === "clear") {
      setLog([]);
      return;
    }
    try {
      const r = await run.mutateAsync(splitArgs(cmd));
      setLog((l) => [...l, { cmd, out: render(r.result) }].slice(-200));
    } catch (err) {
      setLog((l) =>
        [...l, { cmd, out: `(error) ${errText(err)}`, error: true }].slice(
          -200,
        ),
      );
    }
  };
  const keys = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowUp" && hist.length) {
      e.preventDefault();
      const n = Math.min(hi + 1, hist.length - 1);
      setHi(n);
      setLine(hist[n] ?? "");
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      const n = hi - 1;
      setHi(n);
      setLine(n < 0 ? "" : (hist[n] ?? ""));
    }
  };
  return (
    <div className={cn("flex flex-col", gap)}>
      <Panel title="Console" flush>
        <div className="bg-bg h-[55vh] overflow-y-auto p-2 font-mono text-xs">
          {log.length === 0 && (
            <p className="text-faint">
              Runs on the primary as an administrator. Try PING, INFO keyspace,
              SCAN 0, HGETALL user:1. Administration (CONFIG, ACL, REPLICAOF,
              SHUTDOWN…) and blocking or streaming commands are refused. ↑/↓ for
              history, “clear” to clear.
            </p>
          )}
          {log.map((x, i) => (
            <div key={i} className="mb-1.5">
              <div className="text-accent">&gt; {x.cmd}</div>
              <pre
                className={cn(
                  "whitespace-pre-wrap break-all",
                  x.error ? "text-bad" : "text-fg",
                )}
              >
                {x.out}
              </pre>
            </div>
          ))}
          <div ref={end} />
        </div>
        <form
          onSubmit={submit}
          className="border-line flex items-center gap-1.5 border-t p-1.5"
        >
          <span className="text-accent font-mono text-xs">&gt;</span>
          <Input
            value={line}
            onChange={(e) => setLine(e.target.value)}
            onKeyDown={keys}
            placeholder="GET greeting"
            className="h-7 flex-1 font-mono"
            autoFocus
            aria-label="Command"
          />
          <Button type="submit" disabled={run.isPending}>
            Run
          </Button>
        </form>
      </Panel>
    </div>
  );
}
