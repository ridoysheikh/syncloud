import { lazy, Suspense, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Play, Power, SquareTerminal } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";

const Terminal = lazy(() => import("@/ui/Terminal").then((m) => ({ default: m.Terminal })));

interface Op {
  method: string;
  path: string;
  id: string;
  tag: string;
  summary: string;
  params: { name: string; in: string; description?: string }[];
  body: boolean;
  public: boolean;
}

function useSpec() {
  return useQuery({
    queryKey: ["openapi"],
    queryFn: async () => {
      const doc = (await fetch("/api/v1/openapi.json").then((r) => r.json())) as {
        paths: Record<string, Record<string, { operationId: string; tags?: string[]; summary?: string; parameters?: Op["params"]; requestBody?: unknown; security?: unknown[] }>>;
      };
      const ops: Op[] = [];
      for (const [path, methods] of Object.entries(doc.paths)) {
        for (const [m, o] of Object.entries(methods)) {
          ops.push({
            method: m.toUpperCase(),
            path,
            id: o.operationId,
            tag: o.tags?.[0] ?? "other",
            summary: o.summary ?? "",
            params: o.parameters ?? [],
            body: !!o.requestBody,
            public: Array.isArray(o.security) && o.security.length === 0,
          });
        }
      }
      return ops.sort((a, b) => a.tag.localeCompare(b.tag) || a.path.localeCompare(b.path));
    },
    staleTime: Infinity,
  });
}

const methodTone: Record<string, "ok" | "info" | "warn" | "bad" | "neutral"> = { GET: "info", POST: "ok", PUT: "warn", DELETE: "bad", PATCH: "warn" };

function copy(text: string) {
  void navigator.clipboard?.writeText(text);
}

/** API & CLI (§7.1): the API reference with copy-as-curl/synctl, synctl
 * downloads and Cloud Shell. */
export function ApiPage() {
  const { data: ops = [] } = useSpec();
  const { data: cli } = useQuery({ queryKey: ["docs", "cli"], queryFn: () => api<{ commands: Record<string, string> }>("GET", "/docs/cli"), staleTime: Infinity });
  const [filter, setFilter] = useState("");
  const [open, setOpen] = useState<string | null>(null);
  const base = location.origin;
  const shown = useMemo(() => {
    const f = filter.toLowerCase();
    return ops.filter((o) => !f || o.path.toLowerCase().includes(f) || o.summary.toLowerCase().includes(f) || o.id.toLowerCase().includes(f) || o.tag.includes(f));
  }, [ops, filter]);
  const tags = [...new Set(shown.map((o) => o.tag))];
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={[]} title="API & CLI" />
      <ShellPanel />
      <Panel title="synctl">
        <div className="flex flex-col gap-1.5 text-xs">
          <span className="text-muted">Install the CLI, then sign in through this dashboard (no keys on your laptop):</span>
          {[`curl -fsSL ${base}/downloads/synctl-linux-amd64 -o synctl && chmod +x synctl && sudo mv synctl /usr/local/bin/`, `synctl login --endpoint ${base}`].map((c) => (
            <div key={c} className="bg-bg border-line flex items-center gap-2 rounded-sm border px-2 py-1 font-mono">
              <span className="flex-1 break-all">{c}</span>
              <button onClick={() => copy(c)} className="text-muted hover:text-fg" aria-label="Copy">
                <Copy className="size-3.5" />
              </button>
            </div>
          ))}
          <span className="text-muted">
            For CI, create a service account with an access key (IAM › Users) and set SYNCLOUD_ENDPOINT, SYNCLOUD_ACCESS_KEY_ID and SYNCLOUD_SECRET_ACCESS_KEY. Signed
            requests use HMAC-SHA256 (SYN1-HMAC-SHA256); the Go SDK is <span className="font-mono">sdk/go</span>, the TypeScript one{" "}
            <span className="font-mono">sdk/typescript</span>.
          </span>
        </div>
      </Panel>
      <Panel title={`API reference · ${ops.length} operations`} actions={<Input value={filter} onChange={(e) => setFilter(e.target.value)} placeholder="filter" className="h-7 w-56" />}>
        <div className="flex flex-col gap-3">
          {tags.map((t) => (
            <div key={t} className="flex flex-col">
              <span className="text-faint mb-1 text-[11px] font-medium tracking-wide uppercase">{t}</span>
              {shown
                .filter((o) => o.tag === t)
                .map((o) => {
                  const key = o.method + o.path;
                  const isOpen = open === key;
                  const query = o.params.filter((p) => p.in === "query");
                  const curl = `curl -sS -X ${o.method} '${base}${o.path}'${o.public ? "" : " -H 'Authorization: Bearer $SYNCLOUD_TOKEN'"}${o.body ? " -H 'Content-Type: application/json' -d '{…}'" : ""}`;
                  const synctl = cli?.commands[o.id];
                  return (
                    <div key={key} className="border-line border-b last:border-b-0">
                      <button onClick={() => setOpen(isOpen ? null : key)} className="hover:bg-hover flex w-full items-center gap-2 px-1 py-1 text-left text-xs">
                        <StatusBadge tone={methodTone[o.method] ?? "neutral"}>{o.method}</StatusBadge>
                        <span className="min-w-0 font-mono break-all">{o.path}</span>
                        <span className="text-muted hidden truncate sm:inline">{o.summary}</span>
                      </button>
                      {isOpen && (
                        <div className="flex flex-col gap-1 px-1 pb-2 text-xs">
                          <span className="text-faint">
                            operation <span className="font-mono">{o.id}</span>
                            {o.public && " · public"}
                          </span>
                          {query.length > 0 && (
                            <span className="text-muted">
                              Query:{" "}
                              {query.map((p) => (
                                <span key={p.name} className="mr-2">
                                  <span className="font-mono">{p.name}</span>
                                  {p.description ? ` (${p.description})` : ""}
                                </span>
                              ))}
                            </span>
                          )}
                          {([["curl", curl], ...(synctl ? [["synctl", synctl]] : [])] as [string, string][]).map(([label, cmd]) => (
                            <div key={label} className="bg-bg border-line flex items-center gap-2 rounded-sm border px-2 py-1 font-mono">
                              <span className="text-faint w-10">{label}</span>
                              <span className="flex-1 break-all">{cmd}</span>
                              <button onClick={() => copy(cmd)} className="text-muted hover:text-fg" aria-label={`Copy as ${label}`}>
                                <Copy className="size-3.5" />
                              </button>
                            </div>
                          ))}
                        </div>
                      )}
                    </div>
                  );
                })}
            </div>
          ))}
        </div>
      </Panel>
    </div>
  );
}

interface Shell {
  taskId: string;
  state: "starting" | "running" | "failed";
  error?: string;
  expiresAt: string;
}

/** Cloud Shell: synctl signed in as you, in a container on the controller. */
export function ShellPanel({ height }: { height?: string } = {}) {
  const qc = useQueryClient();
  const status = useQuery({
    queryKey: ["shell"],
    queryFn: async () => {
      try {
        return await api<Shell>("GET", "/shell");
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return null;
        throw e;
      }
    },
    refetchInterval: (q) => (q.state.data?.state === "starting" ? 1000 : false),
  });
  const start = useMutation({ mutationFn: () => api<Shell>("POST", "/shell"), onSuccess: (s) => qc.setQueryData(["shell"], s) });
  const stop = useMutation({ mutationFn: () => api("DELETE", "/shell"), onSuccess: () => qc.setQueryData(["shell"], null) });
  const s = status.data;
  return (
    <Panel
      title="Cloud Shell"
      actions={
        s ? (
          <Button variant="ghost" onClick={() => stop.mutate()}>
            <Power className="size-3.5" /> Stop
          </Button>
        ) : (
          <Button variant="primary" onClick={() => start.mutate()} disabled={start.isPending}>
            <Play className="size-3.5" /> Start
          </Button>
        )
      }
    >
      {!s && (
        <p className="text-muted flex items-center gap-2 text-xs">
          <SquareTerminal className="size-4" /> A terminal with synctl already signed in as you (your permissions, credentials valid 1 hour). Files in your home
          directory are kept; idle shells stop after 30 minutes.
        </p>
      )}
      {s?.state === "starting" && <p className="text-muted text-xs">Starting…</p>}
      {s?.state === "failed" && <Alert>Cloud Shell failed to start: {s.error || "unknown error"}. Stop it and start again.</Alert>}
      {s?.state === "running" && (
        <Suspense fallback={null}>
          <Terminal taskId={s.taskId} url="/api/v1/shell/exec" height={height} />
        </Suspense>
      )}
      {(start.error || status.error) && <Alert>{(start.error ?? status.error) instanceof ApiError ? (start.error ?? status.error)!.message : "Cloud Shell is unavailable"}</Alert>}
    </Panel>
  );
}
