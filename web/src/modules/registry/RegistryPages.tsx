import { useCallback, useEffect, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Activity,
  Boxes,
  Check,
  Copy,
  Eraser,
  Eye,
  Search,
  Tag,
  Trash2,
} from "lucide-react";
import { usePaged } from "@/lib/paged";
import { api, ApiError } from "@/lib/api";
import { useStreamTopic } from "@/lib/stream";
import { serviceUrl } from "@/lib/workloads";
import { bytes, since } from "@/lib/nodes";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import {
  Alert,
  Button,
  Field,
  IconButton,
  Input,
  StatusBadge,
} from "@/ui/controls";
import { DEFAULT_POLICY, LifecyclePanel } from "./LifecyclePanel";
import { StoragePanel } from "./StoragePanel";
import { UsagePanel, useUsage } from "./UsagePanel";
import { ImageDetails } from "./ImageDetails";
import {
  groupImages,
  isIdTag,
  sourceOf,
  type Image,
  type ImageGroup,
} from "./images";
import { cn, gap } from "@/ui/cn";
import { useLazyList } from "@/ui/paging";
import { Tabs } from "@/ui/Tabs";
import { Dialog } from "@/ui/Dialog";
import { Segmented } from "@/ui/choice";
import { alertDialog, confirmAction, confirmDialog } from "@/ui/dialogs";
import { ErrorBoundary } from "@/ui/ErrorBoundary";

interface Repository {
  name: string;
  tags: number;
  lifecycle: boolean;
  lifecycleDefault: boolean;
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

interface GCDetail {
  repository: string;
  tag: string;
  tags?: string[];
  sizeBytes?: number;
  reason: string;
  /** Runs before outcomes were recorded only list what they deleted. */
  outcome?: "deleted" | "failed" | "in-use" | "skipped" | "summary";
  error?: string;
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
  details: GCDetail[];
}

interface RegistryInfo {
  host: string;
  alias: string;
  login: string;
  selfSigned?: boolean;
  trust?: string;
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
    items: data,
    isLoading,
    error,
    table,
  } = usePaged<RegistryEvent>(
    ["registry", "events", repo ?? ""],
    `/registry/events${repo ? `?repository=${encodeURIComponent(repo)}` : ""}`,
  );
  if (error) return null;
  return (
    <Panel title={repo ? `Activity: ${repo}` : "Recent activity"} flush>
      <DataTable
        {...table}
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
        {data.selfSigned && data.trust && (
          <div className="border-warn/20 bg-warn/10 mb-1 flex flex-col gap-1 rounded-sm border p-2">
            <span className="text-warn">
              The registry has a self-signed certificate (a private network).
              Nodes trust it automatically; run this once on each machine you
              push from:
            </span>
            <div className="flex items-start justify-between gap-2">
              <pre className="max-h-32 overflow-auto font-mono text-[11px] whitespace-pre-wrap">
                {data.trust}
              </pre>
              <CopyText text={data.trust} />
            </div>
          </div>
        )}
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
  const usage = useUsage().data;
  const tags = repos.reduce((n, r) => n + r.tags, 0);
  const pulls = repos.reduce((n, r) => n + r.pulls, 0);
  const lastPush = repos
    .map((r) => r.lastPushedAt)
    .filter((t): t is string => !!t)
    .sort()
    .at(-1);
  const images = usage?.repositories.reduce((n, r) => n + r.images, 0);
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Registry"]} title="Registry" />
      {error && (
        <Alert tone="warn">
          {error instanceof ApiError ? error.message : "Registry unavailable"}
        </Alert>
      )}
      <div className={cn("grid grid-cols-2 md:grid-cols-3 xl:grid-cols-6", gap)}>
        <StatTile label="Repositories" value={repos.length} />
        <StatTile
          label="Images"
          value={images ?? "…"}
          hint={`${tags} tags`}
        />
        <StatTile
          label="Stored"
          value={
            usage ? bytes(usage.storageBytes ?? usage.referencedBytes) : "…"
          }
        />
        <StatTile
          label="Reclaimable"
          value={
            usage?.unreferencedBytes != null
              ? bytes(usage.unreferencedBytes)
              : "—"
          }
          tone={
            usage?.unreferencedBytes != null &&
            usage.storageBytes &&
            usage.unreferencedBytes > usage.storageBytes / 4
              ? "warn"
              : undefined
          }
          hint="freed by a cleanup"
        />
        <StatTile label="Pulls" value={pulls} />
        <StatTile label="Last push" value={lastPush ? since(lastPush) : "—"} />
      </div>
      <UsagePanel />
      <div className={cn("grid grid-cols-1 xl:grid-cols-2", gap)}>
        <PushCommands />
        <ActivityPanel />
      </div>
      <StoragePanel />
      <Cleanup />
    </div>
  );
}

const outcomeOf = (d: GCDetail) => d.outcome ?? "deleted";

/** What a cleanup did to each image it looked at, and why it did not delete what it could not. */
function RunDialog({ run, onClose }: { run: GCRun | null; onClose: () => void }) {
  const all = run?.details ?? [];
  // The per-repository notes come first, then what happened to each image.
  const rows = [
    ...all.filter((d) => ["summary", "skipped"].includes(outcomeOf(d))),
    ...all.filter((d) => !["summary", "skipped"].includes(outcomeOf(d))),
  ];
  const failed = rows.filter((d) => outcomeOf(d) === "failed").length;
  const kept = rows.filter((d) => outcomeOf(d) === "in-use").length;
  return (
    <Dialog
      open={!!run}
      onClose={onClose}
      size="xl"
      title="Cleanup"
      description={
        run &&
        `${run.trigger} run ${since(run.startedAt)} · ${run.expired} deleted${
          failed ? `, ${failed} failed` : ""
        }${kept ? `, ${kept} kept because they are in use` : ""} · ${bytes(run.reclaimedBytes)} reclaimed`
      }
      footer={<Button onClick={onClose}>Close</Button>}
    >
      <div className="flex flex-col gap-2">
        {run?.message && <Alert>{run.message}</Alert>}
        <DataTable
          rows={rows}
          rowKey={(d) => `${d.repository}@${d.tag}@${outcomeOf(d)}`}
          empty={
            <EmptyState icon={Eraser} title="Nothing expired">
              No image matched a lifecycle rule. Images with no rule are kept;
              layers of untagged images were still collected.
            </EmptyState>
          }
          columns={[
            {
              header: "Result",
              cell: (d) => {
                const o = outcomeOf(d);
                return (
                  <StatusBadge
                    tone={
                      o === "deleted"
                        ? "ok"
                        : o === "failed"
                          ? "bad"
                          : o === "skipped"
                            ? "warn"
                            : o === "summary"
                              ? "neutral"
                              : "info"
                    }
                  >
                    {o === "in-use" ? "kept: in use" : o === "summary" ? "repository" : o}
                  </StatusBadge>
                );
              },
            },
            {
              header: "Image",
              cell: (d) => (
                <span className="font-mono">
                  {d.repository}
                  {d.tag && `:${(d.tags?.length ? d.tags : [d.tag]).join(", ")}`}
                </span>
              ),
            },
            {
              header: "Size",
              cell: (d) => (d.sizeBytes ? bytes(d.sizeBytes) : "—"),
            },
            {
              header: "Why",
              className: "w-full",
              cell: (d) => (
                <span
                  className={cn(
                    "whitespace-normal",
                    d.error ? "text-bad" : "text-muted",
                  )}
                >
                  {d.error ? `${d.error} (${d.reason})` : d.reason}
                </span>
              ),
            },
          ]}
        />
      </div>
    </Dialog>
  );
}

/** Lifecycle + garbage collection runs, with "Clean up now" (§5.10). */
function Cleanup() {
  const qc = useQueryClient();
  const [open, setOpen] = useState<GCRun | null>(null);
  const [defaultOpen, setDefaultOpen] = useState(false);
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
  const count = (r: GCRun, o: string) =>
    r.details.filter((d) => outcomeOf(d) === o).length;
  return (
    <Panel
      title="Cleanup"
      flush
      actions={
        <>
        <Button onClick={() => setDefaultOpen(true)}>Default policy</Button>
        <Button
          onClick={async () =>
            (await confirmAction(
              "Apply lifecycle policies and collect garbage now? Pushes are refused for the minute it runs; pulls keep working.",
            )) && start.mutate()
          }
          disabled={data?.running || start.isPending}
        >
          <Eraser className="size-3.5" />
          {data?.running ? "Running…" : "Clean up now"}
        </Button>
        </>
      }
    >
      <RunDialog run={open} onClose={() => setOpen(null)} />
      <Dialog
        open={defaultOpen}
        onClose={() => setDefaultOpen(false)}
        size="xl"
        title="Default policy"
        description="Applies to every repository without a policy of its own."
        footer={<Button onClick={() => setDefaultOpen(false)}>Close</Button>}
      >
        {defaultOpen && <LifecyclePanel repo={DEFAULT_POLICY} />}
      </Dialog>
      <p className="text-muted px-3 py-2 text-xs">
        Every {data?.everyHours ?? 24} hours: lifecycle policies delete expired
        images, then garbage collection frees their layers and untagged images.
        Only repositories with a lifecycle policy (their own, or the default)
        are cleaned. Open a run to see what happened to each repository and
        image, and why anything was kept or could not be deleted.
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
        rows={runs}
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
            header: "Deleted",
            cell: (r) => (
              <span className="flex items-center gap-1.5">
                {r.expired}
                {count(r, "failed") > 0 && (
                  <StatusBadge tone="bad">
                    {count(r, "failed")} failed
                  </StatusBadge>
                )}
                {count(r, "in-use") > 0 && (
                  <StatusBadge tone="info">
                    {count(r, "in-use")} in use
                  </StatusBadge>
                )}
              </span>
            ),
          },
          { header: "Reclaimed", cell: (r) => bytes(r.reclaimedBytes) },
          {
            header: "Message",
            className: "w-full",
            cell: (r) => (
              <span
                className={cn(
                  "block max-w-xl truncate",
                  r.status === "failed" ? "text-bad" : "text-muted",
                )}
                title={r.message}
              >
                {r.message || "—"}
              </span>
            ),
          },
          {
            header: "",
            cell: (r) => (
              <IconButton label="Details" onClick={() => setOpen(r)}>
                <Eye className="size-3.5" />
              </IconButton>
            ),
          },
        ]}
      />
    </Panel>
  );
}

/**
 * Deletes an image (every tag of it goes). A service running it asks for
 * confirmation; any other failure is shown with the registry's reason.
 */
async function deleteImage(repo: string, tag: string): Promise<boolean> {
  const path = (force: boolean) =>
    `/registry/images?repository=${encodeURIComponent(repo)}&tag=${encodeURIComponent(tag)}${force ? "&force=1" : ""}`;
  let err: unknown;
  try {
    await api("DELETE", path(false));
    return true;
  } catch (e) {
    err = e;
  }
  if (err instanceof ApiError && err.status === 409) {
    const ok = await confirmDialog({
      title: `${repo}:${tag} is in use`,
      message: err.message,
      tone: "danger",
      confirmLabel: "Delete anyway",
    });
    if (!ok) return false;
    try {
      await api("DELETE", path(true));
      return true;
    } catch (e) {
      err = e;
    }
  }
  await alertDialog({
    title: `Could not delete ${repo}:${tag}`,
    message: err instanceof ApiError ? err.message : "The request failed.",
    tone: "danger",
  });
  return false;
}

/** Gives an image another tag: a release named without a rebuild. */
function TagDialog({
  repo,
  group,
  onClose,
}: {
  repo: string;
  group: ImageGroup | null;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [tag, setTag] = useState("");
  useEffect(() => setTag(""), [group]);
  const add = useMutation({
    mutationFn: () =>
      api(
        "POST",
        `/registry/images/tag?repository=${encodeURIComponent(repo)}`,
        { from: group?.tags[0], tag: tag.trim() },
      ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["registry"] });
      onClose();
    },
  });
  const valid = /^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/.test(tag.trim());
  return (
    <Dialog
      open={!!group}
      onClose={onClose}
      title="Add a tag"
      description={group && `${repo}:${group.tags[0]}`}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            type="submit"
            form="tag-image"
            disabled={!valid || add.isPending}
          >
            {add.isPending ? "Tagging…" : "Add tag"}
          </Button>
        </>
      }
    >
      <form
        id="tag-image"
        className="flex flex-col gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (valid) add.mutate();
        }}
      >
        <Field
          label="New tag"
          hint="Nothing is copied: the tag points at the same image. An existing tag with this name moves here."
        >
          <Input
            autoFocus
            value={tag}
            onChange={(e) => setTag(e.target.value)}
            placeholder="v1.4.0"
            className="font-mono"
          />
        </Field>
        {group && group.tags.length > 0 && (
          <div className="text-muted text-xs">
            Now tagged: <span className="font-mono">{group.tags.join(", ")}</span>
          </div>
        )}
        {add.error && (
          <Alert>
            {add.error instanceof ApiError ? add.error.message : "Could not tag"}
          </Alert>
        )}
      </form>
    </Dialog>
  );
}

const SHOWN_TAGS = 3;

function TagChips({ tags, current }: { tags: string[]; current: string }) {
  return (
    <span className="flex items-center gap-1">
      {tags.slice(0, SHOWN_TAGS).map((t) => (
        <span
          key={t}
          className={cn(
            "border-line bg-raised inline-flex h-5 items-center gap-1 rounded-sm border px-1.5 font-mono",
            isIdTag(t) && "text-muted",
          )}
        >
          {t}
          <CopyText text={`@registry/${current}:${t}`} />
        </span>
      ))}
      {tags.length > SHOWN_TAGS && (
        <span
          className="text-faint"
          title={tags.slice(SHOWN_TAGS).join("\n")}
        >
          +{tags.length - SHOWN_TAGS}
        </span>
      )}
    </span>
  );
}

type Filter = "all" | "used" | "unused";

/** Repositories and their images (§5.10). */
export function RepositoriesPage() {
  const qc = useQueryClient();
  useRegistryEvents();
  const { data: repos = [], isLoading, error } = useRepos();
  const usage = useUsage().data;
  const [repo, setRepo] = useState(
    () => new URLSearchParams(window.location.search).get("repo") ?? "",
  );
  const [tab, setTab] = useState<"images" | "activity" | "lifecycle">("images");
  const [q, setQ] = useState("");
  const [filter, setFilter] = useState<Filter>("all");
  const [details, setDetails] = useState<string | null>(null);
  const [tagging, setTagging] = useState<ImageGroup | null>(null);
  const [notice, setNotice] = useState("");
  const [repoQ, setRepoQ] = useState("");
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
  const groups = groupImages(images.data ?? []);
  const shown = groups.filter((g) => {
    if (filter === "used" && g.inUseBy.length === 0) return false;
    if (filter === "unused" && (g.inUseBy.length > 0 || g.cache)) return false;
    const needle = q.trim().toLowerCase();
    return (
      !needle ||
      g.tags.some((t) => t.toLowerCase().includes(needle)) ||
      g.digest.includes(needle) ||
      (sourceOf(g)?.ref ?? "").toLowerCase().includes(needle)
    );
  });
  const sizes = new Map(usage?.repositories.map((r) => [r.name, r]));
  const lazyRepos = useLazyList(
    repos.filter((r) => r.name.includes(repoQ.trim().toLowerCase())),
  );
  const cur = sizes.get(current);

  const remove = async (g: ImageGroup) => {
    const label = g.tags.length > 1 ? `${g.tags[0]} (+${g.tags.length - 1} tags)` : g.tags[0];
    if (
      !(await confirmAction(
        `Delete ${current}:${label}? ${g.tags.length > 1 ? `All ${g.tags.length} tags (${g.tags.join(", ")}) point at this image and go with it. ` : ""}Disk space is freed by the next cleanup.`,
      ))
    )
      return;
    if (await deleteImage(current, g.tags[0]!)) {
      setNotice(
        `Deleted ${current}:${g.tags.join(", ")}. The layers stay on disk until the next cleanup collects them.`,
      );
      void qc.invalidateQueries({ queryKey: ["registry"] });
    }
  };

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Registry"]} title="Repositories" />
      {error && (
        <Alert tone="warn">
          {error instanceof ApiError ? error.message : "Registry unavailable"}
        </Alert>
      )}
      <div className={cn("grid lg:grid-cols-[18rem_1fr]", gap)}>
        <Panel title={`Repositories (${repos.length})`} flush>
          {repos.length === 0 && !isLoading ? (
            <EmptyState icon={Boxes} title="No images yet">
              Push one with the commands on the Registry page.
            </EmptyState>
          ) : (
            <>
              {repos.length > 8 && (
                <div className="border-line border-b p-1.5">
                  <Input
                    value={repoQ}
                    onChange={(e) => setRepoQ(e.target.value)}
                    placeholder="Filter"
                    className="h-7 text-xs"
                  />
                </div>
              )}
              <ul className="text-xs">
                {lazyRepos.shown.map((r) => (
                  <li key={r.name}>
                    <button
                      onClick={() => {
                        setRepo(r.name);
                        setNotice("");
                      }}
                      className={cn(
                        "hover:bg-hover flex w-full items-center justify-between gap-2 px-3 py-1.5 text-left",
                        r.name === current && "bg-hover text-accent",
                      )}
                    >
                      <span className="min-w-0 truncate font-mono">
                        {r.name}
                      </span>
                      <span
                        className="text-faint flex shrink-0 items-center gap-1.5"
                        title={
                          r.lastPulledAt
                            ? `last pulled ${since(r.lastPulledAt)}`
                            : "never pulled"
                        }
                      >
                        {r.lifecycle ? (
                          <span
                            title={
                              r.lifecycleDefault
                                ? "the default lifecycle policy applies"
                                : "lifecycle policy"
                            }
                            className={cn(
                              "size-1.5 rounded-full",
                              r.lifecycleDefault ? "bg-info" : "bg-ok",
                            )}
                          />
                        ) : (
                          <span
                            title="no lifecycle policy: never cleaned"
                            className="bg-warn size-1.5 rounded-full"
                          />
                        )}
                        {sizes.get(r.name) && (
                          <span className="font-mono">
                            {bytes(sizes.get(r.name)!.bytes)}
                          </span>
                        )}
                        <span>{r.tags}</span>
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
              {lazyRepos.more}
            </>
          )}
        </Panel>
        <div className={cn("flex min-w-0 flex-col", gap)}>
          {current && (
            <>
              <div className="grid grid-cols-2 gap-2 md:grid-cols-4">
                <StatTile label="Images" value={cur?.images ?? groups.filter((g) => !g.cache).length} hint={`${images.data?.length ?? 0} tags`} />
                <StatTile label="Size" value={cur ? bytes(cur.bytes) : "…"} hint="layers counted once" />
                <StatTile label="Only here" value={cur ? bytes(cur.uniqueBytes) : "…"} hint={cur ? `${bytes(cur.sharedBytes)} shared` : undefined} />
                <StatTile label="Build cache" value={cur ? bytes(cur.cacheBytes) : "…"} />
              </div>
              <Tabs
                tabs={["images", "activity", "lifecycle"] as const}
                value={tab}
                onChange={setTab}
                capitalize={false}
                label={(t) =>
                  ({
                    images: "Images",
                    activity: "Activity",
                    lifecycle: "Lifecycle policy",
                  })[t]
                }
              />
            </>
          )}
          <ErrorBoundary resetKey={`${current}/${tab}`}>
            {tab === "lifecycle" && current ? (
              <LifecyclePanel key={current} repo={current} />
            ) : tab === "activity" && current ? (
              <ActivityPanel repo={current} />
            ) : (
              <Panel
                title={current || "Images"}
                flush
                actions={
                  <>
                    <Segmented<Filter>
                      size="sm"
                      label="Show"
                      value={filter}
                      onChange={setFilter}
                      options={[
                        { value: "all", label: "All" },
                        { value: "used", label: "In use" },
                        { value: "unused", label: "Unused" },
                      ]}
                    />
                  </>
                }
              >
                <div className="border-line flex items-center gap-2 border-b p-1.5 md:px-3">
                  <div className="relative w-full max-w-xs">
                    <Search className="text-faint pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2" />
                    <Input
                      value={q}
                      onChange={(e) => setQ(e.target.value)}
                      placeholder="Search tag, branch or digest"
                      className="h-7 pl-7 text-xs"
                    />
                  </div>
                  <span className="text-faint text-xs">
                    {shown.length} of {groups.length}
                  </span>
                </div>
                {notice && (
                  <div className="p-1.5 md:px-3">
                    <Alert tone="info">{notice}</Alert>
                  </div>
                )}
                {images.error && (
                  <div className="p-1.5 md:px-3">
                    <Alert>
                      {images.error instanceof ApiError
                        ? images.error.message
                        : "Could not list images"}
                    </Alert>
                  </div>
                )}
                <DataTable
                  rows={shown}
                  rowKey={(g) => g.key}
                  empty={
                    current &&
                    !images.isLoading && (
                      <EmptyState icon={Boxes} title="No images">
                        {groups.length ? "Nothing matches the filter." : undefined}
                      </EmptyState>
                    )
                  }
                  columns={[
                    {
                      header: "Image",
                      cell: (g) => (
                        <span className="flex items-center gap-2">
                          <TagChips tags={g.tags} current={current} />
                          {g.cache && (
                            <StatusBadge tone="info">build cache</StatusBadge>
                          )}
                        </span>
                      ),
                    },
                    {
                      header: "Digest",
                      cell: (g) => (
                        <span className="text-muted font-mono" title={g.digest}>
                          {g.digest.slice(7, 19) || "—"}
                        </span>
                      ),
                    },
                    {
                      header: "Size",
                      cell: (g) => (
                        <span
                          title={`Deleting it frees about ${bytes(g.uniqueBytes)} here (layers nothing else uses)`}
                        >
                          {bytes(g.sizeBytes)}
                          <span className="text-faint">
                            {" "}
                            · {bytes(g.uniqueBytes)} own
                          </span>
                        </span>
                      ),
                    },
                    {
                      header: "Built from",
                      cell: (g) => {
                        const s = sourceOf(g);
                        return s ? (
                          <span className="font-mono">
                            {s.ref}
                            <span className="text-faint"> @{s.sha}</span>
                          </span>
                        ) : (
                          <span className="text-faint">pushed</span>
                        );
                      },
                    },
                    {
                      header: "Pushed",
                      cell: (g) => (
                        <span
                          className="text-muted"
                          title={g.created ? `built ${since(g.created)}` : ""}
                        >
                          {since(g.pushedAt ?? g.created)}
                        </span>
                      ),
                    },
                    {
                      header: "Last pull",
                      cell: (g) => (
                        <span
                          className="text-muted whitespace-nowrap"
                          title={`${g.pulls} pulls`}
                        >
                          {g.lastPulledAt ? since(g.lastPulledAt) : "never"}
                          {g.pulls > 0 && (
                            <span className="text-faint"> · {g.pulls}×</span>
                          )}
                        </span>
                      ),
                    },
                    {
                      header: "In use by",
                      className: "w-full",
                      cell: (g) =>
                        g.inUseBy.length === 0 ? (
                          <span className="text-faint">—</span>
                        ) : (
                          <span className="flex flex-wrap gap-x-2">
                            {g.inUseBy.map((u) => {
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
                      cell: (g) => (
                        <span className="flex items-center justify-end">
                          <IconButton
                            label="Details"
                            onClick={() => setDetails(g.tags[0]!)}
                          >
                            <Eye className="size-3.5" />
                          </IconButton>
                          {!g.cache && (
                            <IconButton
                              label="Add a tag"
                              onClick={() => setTagging(g)}
                            >
                              <Tag className="size-3.5" />
                            </IconButton>
                          )}
                          <IconButton
                            label="Delete image"
                            onClick={() => void remove(g)}
                          >
                            <Trash2 className="size-3.5" />
                          </IconButton>
                        </span>
                      ),
                    },
                  ]}
                />
              </Panel>
            )}
          </ErrorBoundary>
        </div>
      </div>
      <ImageDetails
        repo={current}
        tag={details}
        onClose={() => setDetails(null)}
      />
      <TagDialog
        repo={current}
        group={tagging}
        onClose={() => setTagging(null)}
      />
    </div>
  );
}
