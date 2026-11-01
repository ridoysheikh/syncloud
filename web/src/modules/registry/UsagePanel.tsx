import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { HardDrive, RefreshCw } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { bytes, since } from "@/lib/nodes";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Input, StatusBadge } from "@/ui/controls";
import { cn } from "@/ui/cn";

export interface RepoUsage {
  name: string;
  system: boolean;
  images: number;
  tags: number;
  bytes: number;
  uniqueBytes: number;
  sharedBytes: number;
  cacheBytes: number;
}

export interface Usage {
  repositories: RepoUsage[];
  referencedBytes: number;
  cacheBytes: number;
  storageBytes: number | null;
  unreferencedBytes: number | null;
  measuredAt: string;
}

/** Where the registry's storage goes; walking every tag takes a moment, so it is kept for a minute. */
export function useUsage() {
  return useQuery({
    queryKey: ["registry", "usage"],
    queryFn: () => api<Usage>("GET", "/registry/usage"),
    staleTime: 30_000,
    retry: false,
  });
}

// Module routes are not in the router's static types.
const reposRoute: string = "/registry/repos";

const segments = [
  { key: "images", label: "Images", color: "bg-accent" },
  { key: "cache", label: "Build cache", color: "bg-info" },
  { key: "free", label: "Unreferenced", color: "bg-warn" },
] as const;

/** Storage broken down: what tagged images hold, the builders' cache, and what no tag reaches (freed by a cleanup). */
export function UsagePanel() {
  const qc = useQueryClient();
  const { data: u, error, isFetching } = useUsage();
  const [q, setQ] = useState("");
  const refresh = async () => {
    qc.setQueryData(
      ["registry", "usage"],
      await api<Usage>("GET", "/registry/usage?refresh=1"),
    );
  };
  const [busy, setBusy] = useState(false);
  if (error)
    return error instanceof ApiError && error.status === 404 ? null : (
      <Alert tone="warn">
        {error instanceof ApiError ? error.message : "Usage unavailable"}
      </Alert>
    );

  const images = u ? u.referencedBytes - u.cacheBytes : 0;
  const parts = {
    images,
    cache: u?.cacheBytes ?? 0,
    free: u?.unreferencedBytes ?? 0,
  };
  const total = parts.images + parts.cache + parts.free;
  const repos = (u?.repositories ?? []).filter((r) =>
    r.name.toLowerCase().includes(q.trim().toLowerCase()),
  );
  const max = Math.max(1, ...(u?.repositories ?? []).map((r) => r.bytes));

  return (
    <Panel
      title="Storage"
      flush
      actions={
        <>
          {u && (
            <span className="text-faint mr-1 text-xs">
              measured {since(u.measuredAt)}
            </span>
          )}
          <Button
            disabled={busy || isFetching}
            onClick={async () => {
              setBusy(true);
              try {
                await refresh();
              } finally {
                setBusy(false);
              }
            }}
          >
            <RefreshCw
              className={cn("size-3.5", (busy || isFetching) && "animate-spin")}
            />
            Measure
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-2 p-2 md:p-3">
        <div className="flex items-end justify-between gap-2">
          <div>
            <div className="font-mono text-xl tabular-nums">
              {u ? bytes(u.storageBytes ?? u.referencedBytes) : "…"}
            </div>
            <div className="text-muted text-xs">
              {u?.storageBytes != null
                ? "on the registry's storage"
                : "in tagged images (the storage itself cannot be measured now)"}
            </div>
          </div>
        </div>
        <div
          className="bg-raised flex h-2 overflow-hidden rounded-sm"
          role="img"
          aria-label="Storage by kind"
        >
          {segments.map((s) => (
            <div
              key={s.key}
              className={s.color}
              style={{ width: `${total ? (parts[s.key] / total) * 100 : 0}%` }}
              title={`${s.label}: ${bytes(parts[s.key])}`}
            />
          ))}
        </div>
        <div className="grid grid-cols-1 gap-x-4 gap-y-1 text-xs sm:grid-cols-3">
          {segments.map((s) => (
            <div key={s.key} className="min-w-0">
              <div className="flex items-center gap-1.5">
                <span className={cn("size-2 rounded-sm", s.color)} />
                <span className="text-muted">{s.label}</span>
                <span className="ml-auto font-mono tabular-nums">
                  {s.key === "free" && u?.unreferencedBytes == null
                    ? "—"
                    : bytes(parts[s.key])}
                </span>
              </div>
              <div className="text-faint pl-3.5">
                {
                  {
                    images: "layers of tagged images, each counted once",
                    cache: "kept by builds to speed up the next one",
                    free: "untagged images and orphaned layers: the next cleanup frees them",
                  }[s.key]
                }
              </div>
            </div>
          ))}
        </div>
      </div>
      <div className="border-line border-t p-2 md:px-3">
        <Input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Filter repositories"
          className="h-7 max-w-xs text-xs"
        />
      </div>
      <DataTable
        rows={repos}
        rowKey={(r) => r.name}
        empty={
          u && (
            <EmptyState icon={HardDrive} title="Nothing stored">
              {q ? "No repository matches." : "Push an image to see it here."}
            </EmptyState>
          )
        }
        columns={[
          {
            header: "Repository",
            cell: (r) => (
              <span className="flex items-center gap-1.5">
                <Link
                  to={reposRoute}
                  search={{ repo: r.name } as never}
                  className="hover:text-accent font-mono"
                >
                  {r.name}
                </Link>
                {r.system && <StatusBadge tone="neutral">platform</StatusBadge>}
              </span>
            ),
          },
          {
            header: "Images",
            cell: (r) => (
              <span title={`${r.tags} tags`}>{r.images}</span>
            ),
          },
          {
            header: "Size",
            className: "w-full min-w-40",
            cell: (r) => (
              <div className="flex items-center gap-2">
                <div className="bg-raised flex h-1.5 w-full max-w-48 overflow-hidden rounded-sm">
                  <div
                    className="bg-accent h-full"
                    style={{
                      width: `${((r.bytes - r.cacheBytes) / max) * 100}%`,
                    }}
                  />
                  <div
                    className="bg-info h-full"
                    style={{ width: `${(r.cacheBytes / max) * 100}%` }}
                  />
                </div>
                <span className="font-mono tabular-nums">{bytes(r.bytes)}</span>
              </div>
            ),
          },
          {
            header: "Only here",
            cell: (r) => (
              <span
                className="text-muted font-mono"
                title="layers no other repository uses"
              >
                {bytes(r.uniqueBytes)}
              </span>
            ),
          },
          {
            header: "Shared",
            cell: (r) => (
              <span
                className="text-muted font-mono"
                title="layers other repositories use too: stored once"
              >
                {bytes(r.sharedBytes)}
              </span>
            ),
          },
          {
            header: "Build cache",
            cell: (r) => (
              <span className="text-muted font-mono">
                {r.cacheBytes ? bytes(r.cacheBytes) : "—"}
              </span>
            ),
          },
        ]}
      />
    </Panel>
  );
}
