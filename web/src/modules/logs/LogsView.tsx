import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { History, Loader2, Pause, Play, ScrollText } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Panel } from "@/ui/Panel";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Input } from "@/ui/controls";
import { cn } from "@/ui/cn";

export interface LogLine {
  time: string;
  project: string;
  environment: string;
  service: string;
  taskId: string;
  node: string;
  stream: string;
  level?: string;
  message: string;
}

export interface LogFilter {
  project?: string;
  environment?: string;
  service?: string;
  task?: string;
  /** A database's members (exact, including standalone databases). */
  database?: string;
}

const MAX_LINES = 5000;
const PAGE = 500;

/** Seconds in a range such as "15m" or "168h". */
const secondsOf = (r: string) => Number(r.slice(0, -1)) * (r.endsWith("m") ? 60 : 3600);
const taskColors = ["text-sky-400", "text-violet-400", "text-teal-400", "text-amber-400", "text-pink-400", "text-lime-400"];
const levelColor: Record<string, string> = { fatal: "text-bad", error: "text-bad", warn: "text-warn", debug: "text-faint" };

function colorOf(id: string) {
  let h = 0;
  for (const c of id) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return taskColors[h % taskColors.length];
}

/** Merged logs from every node (§9.2): history from VictoriaLogs plus a live tail. */
export function LogsView({ filter, showSource = true }: { filter: LogFilter; showSource?: boolean }) {
  const [since, setSince] = useState("1h");
  const [text, setText] = useState("");
  const [applied, setApplied] = useState("");
  const [live, setLive] = useState(true);
  const [tail, setTail] = useState<LogLine[]>([]);
  const box = useRef<HTMLDivElement>(null);
  const params = useMemo(() => {
    const p = new URLSearchParams();
    if (filter.project) p.set("project", filter.project);
    if (filter.environment) p.set("environment", filter.environment);
    if (filter.service) p.set("service", filter.service);
    if (filter.task) p.set("task", filter.task);
    if (filter.database) p.set("database", filter.database);
    if (applied) p.set("q", applied);
    return p;
  }, [filter.project, filter.environment, filter.service, filter.task, filter.database, applied]);

  const history = useQuery({
    queryKey: ["logs", params.toString(), since],
    queryFn: async () => (await api<{ items: LogLine[] }>("GET", `/logs?${params}&since=${since}&limit=1000`)).items,
    retry: false,
  });

  // Older lines, a page at a time (§14): each request covers the selected
  // range ending just before the oldest line shown; an empty range moves
  // the cursor back one range.
  const [older, setOlder] = useState<LogLine[]>([]);
  const [cursor, setCursor] = useState<string>();
  const [olderBusy, setOlderBusy] = useState(false);
  const [olderNote, setOlderNote] = useState("");
  const [olderErr, setOlderErr] = useState("");
  useEffect(() => {
    setOlder([]);
    setCursor(undefined);
    setOlderNote("");
    setOlderErr("");
  }, [params, since]);
  const anchor = useRef<number | null>(null); // scrollHeight before prepending
  const loadOlder = async () => {
    if (olderBusy) return;
    const first = older[0] ?? history.data?.[0];
    const before = cursor ?? first?.time ?? new Date(Date.now() - secondsOf(since) * 1000).toISOString();
    setOlderBusy(true);
    setOlderErr("");
    try {
      const page = (await api<{ items: LogLine[] }>("GET", `/logs?${params}&since=${since}&limit=${PAGE}&before=${encodeURIComponent(before)}`)).items;
      anchor.current = box.current?.scrollHeight ?? null;
      if (page.length === 0) {
        const back = new Date(new Date(before).getTime() - secondsOf(since) * 1000);
        setCursor(back.toISOString());
        setOlderNote(`No lines between ${back.toLocaleString()} and ${new Date(before).toLocaleString()}.`);
      } else {
        setCursor(page[0]!.time);
        setOlderNote("");
        setOlder((prev) => [...page, ...prev].slice(0, MAX_LINES));
      }
    } catch (e) {
      setOlderErr(e instanceof ApiError ? e.message : "Could not load older lines");
    } finally {
      setOlderBusy(false);
    }
  };

  useEffect(() => {
    setTail([]);
    if (!live) return;
    const es = new EventSource(`/api/v1/logs/tail?${params}`);
    es.onmessage = (e) => {
      const l = JSON.parse(e.data) as LogLine;
      setTail((prev) => (prev.length >= MAX_LINES ? [...prev.slice(-MAX_LINES / 2), l] : [...prev, l]));
    };
    return () => es.close();
  }, [params, live]);

  const lines = useMemo(() => {
    const hist = history.data ?? [];
    const last = hist.at(-1)?.time ?? "";
    const newest = [...hist, ...tail.filter((l) => l.time > last)];
    return [...older.slice(0, Math.max(0, MAX_LINES - newest.length)), ...newest].slice(-MAX_LINES);
  }, [history.data, tail, older]);

  // Keep the reader's place when older lines are prepended.
  useLayoutEffect(() => {
    const el = box.current;
    if (el && anchor.current !== null) {
      el.scrollTop += el.scrollHeight - anchor.current;
      anchor.current = null;
    }
  }, [older]);

  // Follow the bottom while tailing, unless the user scrolled up.
  const stick = useRef(true);
  useEffect(() => {
    const el = box.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [lines]);

  return (
    <Panel
      flush
      title={
        <span className="flex items-center gap-2">
          Logs <span className="text-faint normal-case">{lines.length} lines</span>
        </span>
      }
      actions={
        <>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              setApplied(text.trim());
            }}
          >
            <Input value={text} onChange={(e) => setText(e.target.value)} placeholder="Search text…" className="h-7 w-40 sm:w-56" />
          </form>
          <select value={since} onChange={(e) => setSince(e.target.value)} className="bg-bg border-line h-7 rounded-sm border px-1.5 text-xs">
            {["5m", "15m", "1h", "6h", "24h", "168h"].map((s) => (
              <option key={s} value={s}>
                last {s === "168h" ? "7d" : s}
              </option>
            ))}
          </select>
          <Button variant={live ? "primary" : "default"} onClick={() => setLive(!live)} title="Live tail">
            {live ? <Pause className="size-3.5" /> : <Play className="size-3.5" />} {live ? "Live" : "Paused"}
          </Button>
        </>
      }
    >
      {history.error && (
        <div className="p-2">
          <Alert tone="warn">
            {history.error instanceof ApiError ? history.error.message : "Log history unavailable"}. Live lines still appear below.
          </Alert>
        </div>
      )}
      <div
        ref={box}
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
          // Scrolled to the top: fetch older lines (once per page that had any).
          if (el.scrollTop < 24 && !olderBusy && !olderNote && !olderErr && lines.length > 0) void loadOlder();
        }}
        className="h-[60vh] overflow-auto px-2 py-1 font-mono text-[11px] leading-[1.45]"
      >
        {(lines.length > 0 || olderNote) && (
          <div className="text-faint flex flex-wrap items-center justify-center gap-2 py-1 font-sans text-xs">
            {olderNote && <span>{olderNote}</span>}
            {olderErr && <span className="text-bad">{olderErr}</span>}
            {olderBusy ? (
              <Loader2 className="size-3.5 animate-spin" aria-label="Loading older lines" />
            ) : (
              <Button variant="ghost" onClick={() => void loadOlder()}>
                <History className="size-3.5" /> Load older
              </Button>
            )}
          </div>
        )}
        {lines.length === 0 && !history.isLoading ? (
          <EmptyState icon={ScrollText} title="No log lines">
            Lines from every task of every node appear here as they are written.
          </EmptyState>
        ) : (
          lines.map((l, i) => (
            <div key={`${l.time}-${l.taskId}-${i}`} className="hover:bg-hover flex gap-2 whitespace-pre-wrap break-all">
              <span className="text-faint shrink-0" title={l.time}>
                {new Date(l.time).toLocaleTimeString(undefined, { hour12: false })}
              </span>
              <span className={cn("shrink-0", colorOf(l.taskId))} title={`${l.taskId} on ${l.node}`}>
                {showSource ? `${l.project}/${l.service}` : l.taskId.replace("task_", "").slice(0, 6)}
              </span>
              <span className={cn(levelColor[l.level ?? ""] ?? (l.stream === "stderr" ? "text-muted" : "text-fg"))}>{l.message}</span>
            </div>
          ))
        )}
      </div>
    </Panel>
  );
}
