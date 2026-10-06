import { useCallback, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Activity, Boxes, Check, Copy, Eraser, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useStreamTopic } from "@/lib/stream";
import { serviceUrl } from "@/lib/workloads";
import { bytes, since } from "@/lib/nodes";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, IconButton, StatusBadge } from "@/ui/controls";
import { LifecyclePanel } from "./LifecyclePanel";
import { cn, gap } from "@/ui/cn";

interface Repository {
  name: string;
  tags: number;
  lifecycle: boolean;
  pulls: number;
  lastPushedAt: string | null;
  lastPulledAt: string | null;
}

interface RegistryEvent {
  id: number;
  at: string;
  action: "push" | "pull" | "delete";
  repository: string;
  tag: string;
  digest: string;
  actor: string;
  addr: string;
  userAgent: string;
}

interface GCRun {
  id: string;
  trigger: string;
  status: "running" | "succeeded" | "failed";
  expired: number;
  reclaimedBytes: number;
  message: string;
  startedAt: string;
  finishedAt: string | null;
  details: { repository: string; tag: string; reason: string }[];
}

interface Image {
  tag: string;
  digest: string;
  sizeBytes: number;
  platforms: string[];
  created: string | null;
  pulls: number;
  lastPulledAt: string | null;
  inUseBy: { project: string; environment: string; service: string }[];
}

interface RegistryInfo {
  host: string;
  alias: string;
  login: string;
}

function CopyText({ text }: { text: string }) {
  const [done, setDone] = useState(false);
  return (
    <IconButton
      label="Copy"
      onClick={() => {
        void navigator.clipboard.writeText(text);
        setDone(true);
        setTimeout(() => setDone(false), 1200);
      }}
    >
      {done ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
    </IconButton>
  );
}

/** Refetches registry queries when the registry reports pushes and pulls. */
function useRegistryEvents() {
  const qc = useQueryClient();
  const onEvent = useCallback(
    () => void qc.invalidateQueries({ queryKey: ["registry"] }),
    [qc],
  );
  useStreamTopic("registry.event", onEvent);
}

const actionTone = { push: "ok", pull: "info", delete: "bad" } as const;

/** Recent pushes, pulls and deletes (§5.9 event tracking). */
function ActivityPanel({ repo }: { repo?: string }) {
  const {
    data = [],
    isLoading,
    error,
  } = useQuery({
    queryKey: ["registry", "events", repo ?? ""],
    queryFn: async () =>
      (
        await api<{ items: RegistryEvent[] }>(
          "GET",
          `/registry/events?limit=${repo ? 100 : 15}${repo ? `&repository=${encodeURIComponent(repo)}` : ""}`,
        )
      ).items,
  });
  if (error) return null;
  return (
    <Panel title={repo ? `Activity: ${repo}` : "Recent activity"} flush>
      <DataTable
        rows={data}
        rowKey={(e) => String(e.id)}
        empty={
          !isLoading && (
            <EmptyState icon={Activity} title="No activity yet">
              Pushes, pulls and deletes show up here as they happen.
            </EmptyState>
          )
        }
        columns={[
          {
            header: "When",
            cell: (e) => <span className="text-muted">{since(e.at)}</span>,
          },
          {
            header: "Action",
            cell: (e) => (
              <StatusBadge tone={actionTone[e.action]}>{e.action}</StatusBadge>
            ),
          },
          {
            header: "Image",
            cell: (e) => (
              <span className="font-mono" title={e.digest}>
                {repo ? "" : e.repository}
                {e.tag ? `:${e.tag}` : `@${e.digest.slice(7, 19)}`}
              </span>
            ),
          },
          {
            header: "By",
            className: "w-full",
            cell: (e) => (
              <span className="text-muted" title={e.userAgent}>
                {e.actor || "—"}
                {e.addr && <span className="text-faint"> · {e.addr}</span>}
              </span>
            ),
          },
        ]}
      />
    </Panel>
  );
}

function useRepos() {
  return useQuery({
    queryKey: ["registry", "repos"],
    queryFn: async () =>
      (await api<{ items: Repository[] }>("GET", "/registry/repositories"))
        .items,
    retry: false,
  });
}

function PushCommands() {
  const { data } = useQuery({
    queryKey: ["registry", "info"],
    queryFn: () => api<RegistryInfo>("GET", "/registry/info"),
  });
  if (!data) return null;
  const lines = [
    `docker login ${data.host} -u <access-key-id>`,
    `docker tag my-app ${data.host}/<project>/<name>:<tag>`,
    `docker push ${data.host}/<project>/<name>:<tag>`,
  ];
  return (
    <Panel title="Push an image">
      <div className="flex flex-col gap-1 text-xs">
        {lines.map((l) => (
          <div
            key={l}
            className="bg-bg border-line flex items-center justify-between rounded-sm border px-2 py-1 font-mono"
          >
            <span className="truncate">{l}</span>
            <CopyText text={l} />
          </div>
        ))}
        <p className="text-muted mt-1">
          The password is the access key's secret, or use any user name with a
          personal access token. In a service, refer to images as{" "}
          <span className="text-fg font-mono">
            @registry/&lt;project&gt;/&lt;name&gt;:&lt;tag&gt;
          </span>
          : nodes pull them with short-lived credentials.
        </p>
      </div>
    </Panel>
  );
}

/** Registry overview (§5.10). */
export function RegistryDashboard() {
  useRegistryEvents();
  const { data: repos = [], error } = useRepos();
  const tags = repos.reduce((n, r) => n + r.tags, 0);
  const pulls = repos.reduce((n, r) => n + r.pulls, 0);
  const lastPush = repos
    .map((r) => r.lastPushedAt)
    .filter((t): t is string => !!t)
    .sort()
    .at(-1);
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Registry"]} title="Registry" />
      {error && (
        <Alert tone="warn">
          {error instanceof ApiError ? error.message : "Registry unavailable"}
        </Alert>
      )}
      <div className={cn("grid grid-cols-2 md:grid-cols-4", gap)}>
        <StatTile label="Repositories" value={repos.length} />
        <StatTile label="Tags" value={tags} />
        <StatTile label="Pulls" value={pulls} />
        <StatTile label="Last push" value={lastPush ? since(lastPush) : "—"} />
      </div>
      <div className={cn("grid grid-cols-1 xl:grid-cols-2", gap)}>
        <PushCommands />
        <ActivityPanel />
      </div>
      <Cleanup />
    </div>
  );
}

/** Lifecycle + garbage collection runs, with "Clean up now" (§5.10). */
function Cleanup() {
  const qc = useQueryClient();
  const { data, error } = useQuery({
    queryKey: ["registry", "gc"],
    queryFn: () =>
      api<{ items: GCRun[]; running: boolean; everyHours: number }>(
        "GET",
        "/registry/gc",
      ),
    refetchInterval: (q) => (q.state.data?.running ? 2000 : 30_000),
    retry: false,
  });
  const start = useMutation({
    mutationFn: () => api("POST", "/registry/gc"),
    onSettled: () => qc.invalidateQueries({ queryKey: ["registry"] }),
  });
  if (error) return null; // cleanup needs the registry system task
  const runs = data?.items ?? [];
  return (
    <Panel
      title="Cleanup"
      flush
      actions={
        <Button
          onClick={() =>
            confirm(
              "Apply lifecycle policies and collect garbage now? Pushes are refused for the minute it runs; pulls keep working.",
            ) && start.mutate()
          }
          disabled={data?.running || start.isPending}
        >
          <Eraser className="size-3.5" />
          {data?.running ? "Running…" : "Clean up now"}
        </Button>
      }
    >
      <p className="text-muted px-3 py-2 text-xs">
        Every {data?.everyHours ?? 24} hours: lifecycle policies delete expired
        images, then garbage collection frees their layers and untagged images.
      </p>
      {start.error && (
        <div className="px-3 pb-2">
          <Alert>
            {start.error instanceof ApiError
              ? start.error.message
              : "Could not start"}
          </Alert>
        </div>
      )}
      <DataTable
        rows={runs.slice(0, 10)}
        rowKey={(r) => r.id}
        empty={<EmptyState icon={Eraser} title="No cleanups yet" />}
        columns={[
          {
            header: "Started",
            cell: (r) => (
              <span className="text-muted">{since(r.startedAt)}</span>
            ),
          },
          {
            header: "Status",
            cell: (r) => (
              <StatusBadge
                tone={
                  r.status === "succeeded"
                    ? "ok"
                    : r.status === "failed"
                      ? "bad"
                      : "info"
                }
              >
                {r.status}
              </StatusBadge>
            ),
          },
          {
            header: "Trigger",
            cell: (r) => <span className="text-muted">{r.trigger}</span>,
          },
          {
            header: "Expired",
            cell: (r) => (
              <span
                title={r.details
                  .map((d) => `${d.repository}:${d.tag}`)
                  .join("\n")}
              >
                {r.expired}
              </span>
            ),
          },
          { header: "Reclaimed", cell: (r) => bytes(r.reclaimedBytes) },
          {
            header: "Message",
            className: "w-full",
            cell: (r) => <span className="text-muted">{r.message || "—"}</span>,
          },
        ]}
      />
    </Panel>
  );
}

/** Repositories and their images (§5.10). */
export function RepositoriesPage() {
  const qc = useQueryClient();
  useRegistryEvents();
  const { data: repos = [], isLoading, error } = useRepos();
  const [repo, setRepo] = useState("");
  const [tab, setTab] = useState<"images" | "activity" | "lifecycle">("images");
  const current = repo || repos[0]?.name || "";
  const images = useQuery({
    queryKey: ["registry", "images", current],
    queryFn: async () =>
      (
        await api<{ items: Image[] }>(
          "GET",
          `/registry/images?repository=${encodeURIComponent(current)}`,
        )
      ).items,
    enabled: !!current,
  });
  const del = useMutation({
    mutationFn: (tag: string) =>
      api(
        "DELETE",
        `/registry/images?repository=${encodeURIComponent(current)}&tag=${encodeURIComponent(tag)}`,
      ),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["registry"] }),
  });

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Registry"]} title="Repositories" />
      {error && (
        <Alert tone="warn">
          {error instanceof ApiError ? error.message : "Registry unavailable"}
        </Alert>
      )}
      <div className={cn("grid lg:grid-cols-[16rem_1fr]", gap)}>
        <Panel title={`Repositories (${repos.length})`} flush>
          {repos.length === 0 && !isLoading ? (
            <EmptyState icon={Boxes} title="No images yet">
              Push one with the commands on the Registry page.
            </EmptyState>
          ) : (
            <ul className="text-xs">
              {repos.map((r) => (
                <li key={r.name}>
                  <button
                    onClick={() => setRepo(r.name)}
                    className={cn(
                      "hover:bg-hover flex w-full justify-between px-3 py-1.5 text-left font-mono",
                      r.name === current && "bg-hover text-accent",
                    )}
                  >
                    <span className="truncate">{r.name}</span>
                    <span
                      className="text-faint flex items-center gap-1.5"
                      title={
                        r.lastPulledAt
                          ? `last pulled ${since(r.lastPulledAt)}`
                          : "never pulled"
                      }
                    >
                      {r.lifecycle && (
                        <span
                          title="lifecycle policy"
                          className="bg-ok size-1.5 rounded-full"
                        />
                      )}
                      {r.tags}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Panel>
        <div className={cn("flex min-w-0 flex-col", gap)}>
          {current && (
            <div className="border-line flex gap-3 border-b text-xs">
              {(["images", "activity", "lifecycle"] as const).map((t) => (
                <button
                  key={t}
                  onClick={() => setTab(t)}
                  className={cn(
                    "-mb-px border-b-2 px-1 pb-1.5",
                    tab === t
                      ? "border-accent text-fg"
                      : "text-muted hover:text-fg border-transparent",
                  )}
                >
                  {
                    {
                      images: "Images",
                      activity: "Activity",
                      lifecycle: "Lifecycle policy",
                    }[t]
                  }
                </button>
              ))}
            </div>
          )}
          {tab === "lifecycle" && current ? (
            <LifecyclePanel key={current} repo={current} />
          ) : tab === "activity" && current ? (
            <ActivityPanel repo={current} />
          ) : (
            <Panel title={current || "Images"} flush>
              <DataTable
                rows={images.data ?? []}
                rowKey={(i) => i.tag}
                empty={
                  current &&
                  !images.isLoading && (
                    <EmptyState icon={Boxes} title="No tags" />
                  )
                }
                columns={[
                  {
                    header: "Tag",
                    cell: (i) => (
                      <span className="flex items-center gap-1 font-mono">
                        {i.tag}
                        <CopyText text={`@registry/${current}:${i.tag}`} />
                      </span>
                    ),
                  },
                  {
                    header: "Digest",
                    cell: (i) => (
                      <span className="text-muted font-mono" title={i.digest}>
                        {i.digest.slice(7, 19)}
                      </span>
                    ),
                  },
                  { header: "Size", cell: (i) => bytes(i.sizeBytes) },
                  {
                    header: "Platforms",
                    cell: (i) => (
                      <span className="text-muted">
                        {i.platforms.join(", ") || "—"}
                      </span>
                    ),
                  },
                  {
                    header: "Created",
                    cell: (i) => (
                      <span className="text-muted">
                        {i.created ? since(i.created) : "—"}
                      </span>
                    ),
                  },
                  {
                    header: "Last pull",
                    cell: (i) => (
                      <span
                        className="text-muted whitespace-nowrap"
                        title={`${i.pulls} pulls`}
                      >
                        {i.lastPulledAt ? since(i.lastPulledAt) : "never"}
                        {i.pulls > 0 && (
                          <span className="text-faint"> · {i.pulls}×</span>
                        )}
                      </span>
                    ),
                  },
                  {
                    header: "In use by",
                    className: "w-full",
                    cell: (i) =>
                      i.inUseBy.length === 0 ? (
                        <span className="text-faint">—</span>
                      ) : (
                        <span className="flex flex-wrap gap-x-2">
                          {i.inUseBy.map((u) => {
                            const to: string = serviceUrl({
                              project: u.project,
                              environment: u.environment,
                              name: u.service,
                            });
                            return (
                              <Link
                                key={to}
                                to={to}
                                className="hover:text-accent"
                              >
                                {u.project}/{u.environment}/{u.service}
                              </Link>
                            );
                          })}
                        </span>
                      ),
                  },
                  {
                    header: "",
                    cell: (i) => (
                      <IconButton
                        label="Delete tag"
                        onClick={() =>
                          confirm(
                            `Delete ${current}:${i.tag}? Tags with the same digest are deleted too.`,
                          ) && del.mutate(i.tag)
                        }
                      >
                        <Trash2 className="size-3.5" />
                      </IconButton>
                    ),
                  },
                ]}
              />
            </Panel>
          )}
        </div>
      </div>
    </div>
  );
}
