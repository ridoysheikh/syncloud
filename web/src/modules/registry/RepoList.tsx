import { useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Boxes, Eye, Search, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { bytes, since } from "@/lib/nodes";
import { serviceUrl, useServices } from "@/lib/workloads";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { IconButton, Input, StatusBadge } from "@/ui/controls";
import { Segmented } from "@/ui/choice";
import { Select } from "@/ui/select";
import { alertDialog, confirmDialog } from "@/ui/dialogs";
import { cn, gap } from "@/ui/cn";
import type { RepoUsage } from "./UsagePanel";

/* The registry's repositories at a glance, and deleting a whole repository. */

export interface Repository {
  name: string;
  tags: number;
  lifecycle: boolean;
  lifecycleDefault: boolean;
  pulls: number;
  lastPushedAt: string | null;
  lastPulledAt: string | null;
}

export function useRepos() {
  return useQuery({
    queryKey: ["registry", "repos"],
    queryFn: async () =>
      (await api<{ items: Repository[] }>("GET", "/registry/repositories"))
        .items,
    retry: false,
  });
}

export interface RepoUser {
  project: string;
  environment: string;
  name: string;
}

/** The services that run each repository's images, by repository name. */
export function useRepoUsers(): Map<string, RepoUser[]> {
  const services = useServices().data;
  const host = useQuery({
    queryKey: ["registry", "info"],
    queryFn: () => api<{ host: string }>("GET", "/registry/info"),
    retry: false,
    staleTime: 60_000,
  }).data?.host;
  return useMemo(() => {
    const out = new Map<string, RepoUser[]>();
    for (const s of services ?? []) {
      let ref = s.spec.image ?? "";
      if (ref.startsWith("@registry/")) ref = ref.slice("@registry/".length);
      else if (host && ref.startsWith(`${host}/`)) ref = ref.slice(host.length + 1);
      else continue;
      ref = ref.replace(/@sha256:.*$/, "");
      const colon = ref.lastIndexOf(":");
      const repo = colon > 0 && !ref.slice(colon).includes("/") ? ref.slice(0, colon) : ref;
      const list = out.get(repo) ?? [];
      list.push({ project: s.project, environment: s.environment, name: s.name });
      out.set(repo, list);
    }
    return out;
  }, [services, host]);
}

const path = (u: RepoUser) => `${u.project}/${u.environment}/${u.name}`;

/**
 * Deletes a whole repository after the user types its name. A repository a
 * service runs is refused by the server and asks again before it is forced;
 * any other failure shows the registry's reason.
 */
export async function deleteRepository(
  repo: string,
  info: { tags: number; bytes?: number; users: RepoUser[] },
): Promise<boolean> {
  const running = info.users.length
    ? ` ${info.users.map(path).join(", ")} ${info.users.length === 1 ? "runs" : "run"} its images.`
    : "";
  const ok = await confirmDialog({
    title: `Delete the repository ${repo}?`,
    message: (
      <>
        Every tag goes: {info.tags} {info.tags === 1 ? "tag" : "tags"}
        {info.bytes ? `, about ${bytes(info.bytes)}` : ""}, with the build cache and the
        repository's lifecycle policy.{running} Disk space is freed by the next cleanup.
        This cannot be undone.
      </>
    ),
    tone: "danger",
    confirmLabel: "Delete repository",
    typeToConfirm: repo,
  });
  if (!ok) return false;
  const url = (force: boolean) =>
    `/registry/repositories?repository=${encodeURIComponent(repo)}${force ? "&force=1" : ""}`;
  let err: unknown;
  try {
    await api("DELETE", url(false));
    return true;
  } catch (e) {
    err = e;
  }
  if (err instanceof ApiError && err.status === 409) {
    const again = await confirmDialog({
      title: `${repo} is in use`,
      message: err.message,
      tone: "danger",
      confirmLabel: "Delete anyway",
    });
    if (!again) return false;
    try {
      await api("DELETE", url(true));
      return true;
    } catch (e) {
      err = e;
    }
  }
  await alertDialog({
    title: `Could not delete ${repo}`,
    message: err instanceof ApiError ? err.message : "The request failed.",
    tone: "danger",
  });
  return false;
}

type Filter = "all" | "used" | "unused" | "nopolicy";
type Sort = "name" | "size" | "pushed" | "pulled" | "pulls" | "tags";

const sorts: { value: Sort; label: string }[] = [
  { value: "name", label: "Name" },
  { value: "size", label: "Size" },
  { value: "pushed", label: "Last push" },
  { value: "pulled", label: "Last pull" },
  { value: "pulls", label: "Pulls" },
  { value: "tags", label: "Tags" },
];

const time = (iso: string | null) => (iso ? Date.parse(iso) : 0);

export function RepoOverview({
  repos,
  loading,
  usage,
  users,
  onOpen,
  onDelete,
}: {
  repos: Repository[];
  loading: boolean;
  usage: RepoUsage[] | undefined;
  users: Map<string, RepoUser[]>;
  onOpen: (repo: string) => void;
  onDelete: (repo: Repository) => void;
}) {
  const [q, setQ] = useState("");
  const [filter, setFilter] = useState<Filter>("all");
  const [sort, setSort] = useState<Sort>("name");
  const sizes = useMemo(() => new Map(usage?.map((r) => [r.name, r])), [usage]);

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    const rows = repos.filter((r) => {
      if (needle && !r.name.toLowerCase().includes(needle)) return false;
      const used = (users.get(r.name) ?? []).length > 0;
      if (filter === "used") return used;
      if (filter === "unused") return !used;
      if (filter === "nopolicy") return !r.lifecycle;
      return true;
    });
    const by: Record<Sort, (a: Repository, b: Repository) => number> = {
      name: (a, b) => a.name.localeCompare(b.name),
      size: (a, b) => (sizes.get(b.name)?.bytes ?? 0) - (sizes.get(a.name)?.bytes ?? 0),
      pushed: (a, b) => time(b.lastPushedAt) - time(a.lastPushedAt),
      pulled: (a, b) => time(b.lastPulledAt) - time(a.lastPulledAt),
      pulls: (a, b) => b.pulls - a.pulls,
      tags: (a, b) => b.tags - a.tags,
    };
    return [...rows].sort(by[sort]);
  }, [repos, q, filter, sort, users, sizes]);

  const total = usage?.reduce((n, r) => n + r.bytes, 0);
  const unused = repos.filter((r) => !(users.get(r.name) ?? []).length).length;

  return (
    <div className={cn("flex flex-col", gap)}>
      <div className="grid grid-cols-2 gap-2 md:grid-cols-4">
        <StatTile label="Repositories" value={repos.length} hint={`${repos.reduce((n, r) => n + r.tags, 0)} tags`} />
        <StatTile label="Size" value={total === undefined ? "…" : bytes(total)} hint="layers counted once per repository" />
        <StatTile label="Run by services" value={repos.length - unused} hint={`${unused} not in use`} />
        <StatTile
          label="No cleanup rule"
          value={repos.filter((r) => !r.lifecycle).length}
          hint="never cleaned automatically"
        />
      </div>
      <Panel
        title={`Repositories (${repos.length})`}
        flush
        actions={
          <Segmented<Filter>
            size="sm"
            label="Show"
            value={filter}
            onChange={setFilter}
            options={[
              { value: "all", label: "All" },
              { value: "used", label: "In use" },
              { value: "unused", label: "Unused" },
              { value: "nopolicy", label: "No policy" },
            ]}
          />
        }
      >
        <div className="border-line flex flex-wrap items-center gap-2 border-b p-1.5 md:px-3">
          <div className="relative w-full max-w-xs">
            <Search className="text-faint pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2" />
            <Input
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="Search repositories"
              className="h-7 pl-7 text-xs"
            />
          </div>
          <span className="text-muted flex items-center gap-1.5 text-xs">
            Sort by
            <Select value={sort} onChange={(v) => setSort(v as Sort)} size="sm" aria-label="Sort by">
              {sorts.map((s) => (
                <option key={s.value} value={s.value}>
                  {s.label}
                </option>
              ))}
            </Select>
          </span>
          <span className="text-faint ml-auto text-xs">
            {shown.length} of {repos.length}
          </span>
        </div>
        <DataTable
          rows={shown}
          rowKey={(r) => r.name}
          empty={
            !loading && (
              <EmptyState icon={Boxes} title={repos.length ? "No repository matches" : "No images yet"}>
                {repos.length ? "Change the search or the filter." : "Push one with the commands on the Registry page."}
              </EmptyState>
            )
          }
          columns={[
            {
              header: "Repository",
              cell: (r) => (
                <button
                  onClick={() => onOpen(r.name)}
                  className="hover:text-accent max-w-72 truncate text-left font-mono"
                  title={r.name}
                >
                  {r.name}
                </button>
              ),
            },
            {
              header: "Images",
              cell: (r) => (
                <span title={`${r.tags} tags`}>
                  {sizes.get(r.name)?.images ?? "…"}
                  <span className="text-faint"> · {r.tags} {r.tags === 1 ? "tag" : "tags"}</span>
                </span>
              ),
            },
            {
              header: "Size",
              cell: (r) => {
                const u = sizes.get(r.name);
                return u ? (
                  <span title={`${bytes(u.uniqueBytes)} only here, ${bytes(u.sharedBytes)} shared, ${bytes(u.cacheBytes)} build cache`}>
                    {bytes(u.bytes)}
                    <span className="text-faint"> · {bytes(u.uniqueBytes)} own</span>
                  </span>
                ) : (
                  <span className="text-faint">…</span>
                );
              },
            },
            {
              header: "Last push",
              cell: (r) => <span className="text-muted">{r.lastPushedAt ? since(r.lastPushedAt) : "—"}</span>,
            },
            {
              header: "Last pull",
              cell: (r) => (
                <span className="text-muted" title={`${r.pulls} pulls`}>
                  {r.lastPulledAt ? since(r.lastPulledAt) : "never"}
                  {r.pulls > 0 && <span className="text-faint"> · {r.pulls}×</span>}
                </span>
              ),
            },
            {
              header: "Cleanup",
              cell: (r) =>
                r.lifecycle ? (
                  <StatusBadge tone={r.lifecycleDefault ? "info" : "ok"}>
                    {r.lifecycleDefault ? "default policy" : "own policy"}
                  </StatusBadge>
                ) : (
                  <StatusBadge tone="warn">none</StatusBadge>
                ),
            },
            {
              header: "In use by",
              className: "w-full",
              cell: (r) => {
                const list = users.get(r.name) ?? [];
                if (list.length === 0) return <span className="text-faint">—</span>;
                return (
                  <span className="flex flex-wrap gap-x-2">
                    {list.slice(0, 2).map((u) => (
                      <Link key={path(u)} to={serviceUrl(u) as string} className="hover:text-accent">
                        {path(u)}
                      </Link>
                    ))}
                    {list.length > 2 && (
                      <span className="text-faint" title={list.slice(2).map(path).join(", ")}>
                        +{list.length - 2} more
                      </span>
                    )}
                  </span>
                );
              },
            },
            {
              header: "",
              cell: (r) => (
                <span className="flex items-center justify-end">
                  <IconButton label="Open" onClick={() => onOpen(r.name)}>
                    <Eye className="size-3.5" />
                  </IconButton>
                  {!sizes.get(r.name)?.system && (
                    <IconButton label="Delete repository" onClick={() => onDelete(r)}>
                      <Trash2 className="size-3.5" />
                    </IconButton>
                  )}
                </span>
              ),
            },
          ]}
        />
      </Panel>
    </div>
  );
}
