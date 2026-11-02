import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Box,
  Container,
  Database,
  Eraser,
  HardDrive,
  Layers,
  RefreshCw,
  Search,
  Trash2,
} from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { bytes, since } from "@/lib/nodes";
import { serviceUrl } from "@/lib/workloads";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Tabs } from "@/ui/Tabs";
import { Alert, Button, IconButton, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { confirmDialog } from "@/ui/dialogs";

interface App {
  kind: "service" | "platform" | "managed" | "other";
  name: string;
  project?: string;
  environment?: string;
  service?: string;
  containers: number;
  running: number;
  containerBytes: number;
  volumeBytes: number;
  imageBytes: number;
  bytes: number;
}

interface DockerContainer {
  id: string;
  name: string;
  image: string;
  state: string;
  status: string;
  created: number;
  writableBytes: number;
  logBytes: number;
  managed: boolean;
  system: boolean;
  app: App;
}

interface DockerImage {
  id: string;
  tags: string[];
  created: number;
  bytes: number;
  sharedBytes: number;
  containers: number;
  dangling: boolean;
  apps: string[];
}

interface DockerVolume {
  name: string;
  bytes: number;
  refCount: number;
  anonymous: boolean;
  created: number;
  usedBy: string[];
  apps: string[];
}

interface CacheRecord {
  id: string;
  type: string;
  description: string;
  bytes: number;
  inUse: boolean;
  lastUsed: number;
}

interface NodeDocker {
  node: string;
  nodeName: string;
  dockerVersion: string;
  diskPath: string;
  measuredAt: string;
  totals: {
    imageBytes: number;
    containerBytes: number;
    logBytes: number;
    volumeBytes: number;
    buildCacheBytes: number;
    dockerBytes: number;
    diskTotalBytes: number;
    diskUsedBytes: number;
    diskFreeBytes: number;
    otherBytes: number;
  };
  reclaimable: {
    danglingImages: number;
    unusedImages: number;
    containers: number;
    stoppedManaged: number;
    anonymousVolumes: number;
    unusedVolumes: number;
    buildCache: number;
  };
  apps: App[];
  containers: DockerContainer[];
  images: DockerImage[];
  volumes: DockerVolume[];
  buildCache: CacheRecord[];
  buildCacheCount: number;
}

interface CleanResult {
  reclaimedBytes: number;
  removed: number;
  errors: string[];
}

type Kind = "images" | "containers" | "volumes" | "buildcache";
type Section = "apps" | "containers" | "images" | "volumes" | "cache";

const errText = (e: unknown) =>
  e instanceof ApiError ? e.message : "The request failed.";

const day = (unix: number) => (unix ? since(new Date(unix * 1000).toISOString()) : "—");

const segments = [
  { key: "imageBytes", label: "Images", color: "bg-accent", hint: "layers of every image, shared ones once" },
  { key: "volumeBytes", label: "Volumes", color: "bg-ok", hint: "service, database and registry data" },
  { key: "containerBytes", label: "Containers", color: "bg-warn", hint: "what containers wrote to their own layer" },
  { key: "logBytes", label: "Logs", color: "bg-bad", hint: "container log files, kept until the container goes" },
  { key: "buildCacheBytes", label: "Build cache", color: "bg-accent/50", hint: "image build layers kept for speed" },
  { key: "otherBytes", label: "Not Docker", color: "bg-neutral", hint: "the system, files and everything else on the disk" },
  { key: "diskFreeBytes", label: "Free", color: "bg-raised", hint: "" },
] as const;

/** A node's Docker host: where the disk goes, who uses it, and cleaning it. */
export function NodeDockerTab({ nodeId }: { nodeId: string }) {
  const qc = useQueryClient();
  const key = ["node-docker", nodeId];
  const { data, error, isFetching, refetch } = useQuery({
    queryKey: key,
    queryFn: () => api<NodeDocker>("GET", `/nodes/${nodeId}/docker`),
    staleTime: 60_000,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const [section, setSection] = useState<Section>("apps");
  const [q, setQ] = useState("");
  const [result, setResult] = useState<
    (CleanResult & { what: string }) | null
  >(null);
  const clean = useMutation({
    mutationFn: (v: { kind: Kind; ids?: string[]; all?: boolean; what: string }) =>
      api<CleanResult>("POST", `/nodes/${nodeId}/docker/clean`, {
        kind: v.kind,
        ids: v.ids,
        all: v.all,
      }),
    onSuccess: (r, v) => {
      setResult({ ...r, what: v.what });
      void qc.invalidateQueries({ queryKey: key });
    },
  });
  const run = async (
    what: string,
    kind: Kind,
    opts: {
      ids?: string[];
      all?: boolean;
      message: string;
      typeToConfirm?: string;
      danger?: boolean;
    },
  ) => {
    const ok = await confirmDialog({
      title: what,
      message: opts.message,
      tone: opts.danger === false ? "default" : "danger",
      confirmLabel: "Clean",
      typeToConfirm: opts.typeToConfirm,
    });
    if (ok) clean.mutate({ kind, ids: opts.ids, all: opts.all, what });
  };

  if (error)
    return <Alert tone="warn">{errText(error)}</Alert>;
  if (!data)
    return (
      <Panel title="Docker">
        <div className="text-muted flex items-center gap-2 text-xs">
          <RefreshCw className="size-3.5 animate-spin" />
          Measuring Docker on the node. A node with many containers and
          volumes takes a few seconds.
        </div>
      </Panel>
    );

  const t = data.totals;
  const r = data.reclaimable;
  const safe = r.danglingImages + r.containers + r.anonymousVolumes + r.buildCache;
  const needle = q.trim().toLowerCase();
  const match = (...s: (string | undefined)[]) =>
    !needle || s.some((x) => x?.toLowerCase().includes(needle));
  const diskPct = t.diskTotalBytes ? (t.diskUsedBytes / t.diskTotalBytes) * 100 : 0;

  const cards: {
    icon: typeof Box;
    title: string;
    text: string;
    bytes: number;
    action: () => void;
    danger?: boolean;
  }[] = [
    {
      icon: Layers,
      title: "Untagged images",
      text: "Old layers left behind by updates. Nothing uses them.",
      bytes: r.danglingImages,
      action: () =>
        void run("Remove untagged images?", "images", {
          message: "Images with no tag that no container uses are deleted.",
          danger: false,
        }),
    },
    {
      icon: Layers,
      title: "All unused images",
      text: "Every image no container uses. They are pulled again when a service needs them.",
      bytes: r.unusedImages,
      action: () =>
        void run("Remove all unused images?", "images", {
          all: true,
          message:
            "Every image no container uses is deleted, tagged or not. A service that needs one later pulls it again, which takes time and bandwidth.",
        }),
    },
    {
      icon: Container,
      title: "Stopped containers",
      text: "Containers started outside SynCloud that have exited, with their logs.",
      bytes: r.containers,
      action: () =>
        void run("Remove stopped containers?", "containers", {
          message:
            "Stopped containers that SynCloud did not start are deleted with their log files. SynCloud's own are listed under Containers.",
        }),
    },
    {
      icon: Database,
      title: "Anonymous volumes",
      text: "Volumes with a random name that no container mounts. Named volumes are never bulk-removed.",
      bytes: r.anonymousVolumes,
      action: () =>
        void run("Remove anonymous volumes?", "volumes", {
          message:
            "Volumes with a random name that no container uses are deleted with their data.",
        }),
    },
    {
      icon: HardDrive,
      title: "Build cache",
      text: "Layers kept to speed up builds. The next build is slower, once.",
      bytes: r.buildCache,
      action: () =>
        void run("Clear the build cache?", "buildcache", {
          all: true,
          message: "Cached build layers nothing is building with are deleted.",
          danger: false,
        }),
    },
  ];

  return (
    <div className={cn("flex flex-col", gap)}>
      <div className={cn("grid grid-cols-2 md:grid-cols-5", gap)}>
        <StatTile
          label="Disk used"
          value={bytes(t.diskUsedBytes)}
          hint={`of ${bytes(t.diskTotalBytes)} · ${diskPct.toFixed(0)}%`}
          tone={diskPct >= 90 ? "bad" : diskPct >= 75 ? "warn" : undefined}
        />
        <StatTile label="Docker holds" value={bytes(t.dockerBytes)} hint={data.diskPath} />
        <StatTile label="Not Docker" value={bytes(t.otherBytes)} hint="system and other files" />
        <StatTile label="Free" value={bytes(t.diskFreeBytes)} />
        <StatTile
          label="Safe to clean"
          value={bytes(safe)}
          hint="untagged images, stopped containers, anonymous volumes, cache"
        />
      </div>

      <Panel
        title="Disk"
        actions={
          <>
            <span className="text-faint mr-1 text-xs">
              measured {since(data.measuredAt)}
              {data.dockerVersion && ` · Docker ${data.dockerVersion}`}
            </span>
            <Button onClick={() => void refetch()} disabled={isFetching}>
              <RefreshCw className={cn("size-3.5", isFetching && "animate-spin")} />
              Measure
            </Button>
          </>
        }
      >
        <div
          className="bg-raised flex h-3 overflow-hidden rounded-sm"
          role="img"
          aria-label="Disk by kind"
        >
          {segments.map((s) => (
            <div
              key={s.key}
              className={s.color}
              style={{
                width: `${t.diskTotalBytes ? (t[s.key] / t.diskTotalBytes) * 100 : 0}%`,
              }}
              title={`${s.label}: ${bytes(t[s.key])}`}
            />
          ))}
        </div>
        <div className="mt-2 grid grid-cols-2 gap-x-4 gap-y-1.5 text-xs md:grid-cols-4 xl:grid-cols-7">
          {segments.map((s) => (
            <div key={s.key} className="min-w-0" title={s.hint}>
              <div className="flex items-center gap-1.5">
                <span className={cn("border-line size-2.5 shrink-0 rounded-sm border", s.color)} />
                <span className="text-muted truncate">{s.label}</span>
              </div>
              <div className="pl-4 font-mono tabular-nums">
                {bytes(t[s.key])}
                <span className="text-faint">
                  {" "}
                  {t.diskTotalBytes
                    ? `${((t[s.key] / t.diskTotalBytes) * 100).toFixed(1)}%`
                    : ""}
                </span>
              </div>
            </div>
          ))}
        </div>
      </Panel>

      <Panel title="Clean up">
        {result && (
          <div className="mb-2">
            <Alert tone={result.errors.length ? "warn" : "info"}>
              <div>
                {result.what.replace(/\?$/, "")}: removed {result.removed},
                reclaimed {bytes(result.reclaimedBytes)}.
              </div>
              {result.errors.length > 0 && (
                <ul className="mt-1 list-disc pl-4">
                  {result.errors.slice(0, 8).map((e) => (
                    <li key={e} className="break-all">
                      {e}
                    </li>
                  ))}
                  {result.errors.length > 8 && (
                    <li>…and {result.errors.length - 8} more</li>
                  )}
                </ul>
              )}
            </Alert>
          </div>
        )}
        {clean.error && (
          <div className="mb-2">
            <Alert>{errText(clean.error)}</Alert>
          </div>
        )}
        <div className={cn("grid grid-cols-1 md:grid-cols-2 xl:grid-cols-5", gap)}>
          {cards.map((c) => (
            <div
              key={c.title}
              className="border-line bg-bg flex min-w-0 flex-col gap-1.5 rounded-md border p-2 md:p-3"
            >
              <div className="flex items-center gap-1.5 text-xs font-medium">
                <c.icon className="text-muted size-3.5 shrink-0" />
                <span className="truncate">{c.title}</span>
              </div>
              <div className="font-mono text-lg tabular-nums">{bytes(c.bytes)}</div>
              <p className="text-muted flex-1 text-xs">{c.text}</p>
              <Button
                variant={c.danger ? "danger" : "default"}
                disabled={clean.isPending || c.bytes === 0}
                onClick={c.action}
              >
                <Eraser className="size-3.5" />
                {clean.isPending ? "Cleaning…" : "Clean"}
              </Button>
            </div>
          ))}
        </div>
        <p className="text-faint mt-2 text-xs">
          SynCloud's own containers and named volumes (databases, the registry,
          service data) are never removed in bulk: remove them one by one in
          the lists below, where each shows who uses it. Docker refuses to
          remove anything in use.
        </p>
      </Panel>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <Tabs
          tabs={["apps", "containers", "images", "volumes", "cache"] as Section[]}
          value={section}
          onChange={setSection}
          capitalize={false}
          label={(s) =>
            ({
              apps: `Apps (${data.apps.length})`,
              containers: `Containers (${data.containers.length})`,
              images: `Images (${data.images.length})`,
              volumes: `Volumes (${data.volumes.length})`,
              cache: `Build cache (${data.buildCacheCount})`,
            })[s]
          }
          className="min-w-0 flex-1"
        />
        <div className="relative w-full max-w-60">
          <Search className="text-faint pointer-events-none absolute top-1/2 left-2 size-3.5 -translate-y-1/2" />
          <Input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search"
            className="h-7 pl-7 text-xs"
          />
        </div>
      </div>

      {section === "apps" && <AppsTable data={data} match={match} />}
      {section === "containers" && (
        <ContainersTable
          rows={data.containers.filter((c) => match(c.name, c.image, c.app.name))}
          busy={clean.isPending}
          onDelete={(c) =>
            void run(`Remove ${c.name}?`, "containers", {
              ids: [c.id],
              message: c.managed
                ? `${c.name} is a SynCloud task's container. It is created again when the task starts. Its log (${bytes(c.logBytes)}) and anything it wrote (${bytes(c.writableBytes)}) are lost.`
                : `${c.name} is deleted with its log (${bytes(c.logBytes)}) and anything it wrote (${bytes(c.writableBytes)}).`,
            })
          }
        />
      )}
      {section === "images" && (
        <ImagesTable
          rows={data.images.filter((i) => match(...i.tags, i.id, ...i.apps))}
          busy={clean.isPending}
          onDelete={(i) =>
            void run(`Remove ${i.tags[0] ?? i.id.slice(7, 19)}?`, "images", {
              ids: [i.id],
              message:
                "The image is deleted. A service that needs it later pulls it again.",
            })
          }
        />
      )}
      {section === "volumes" && (
        <VolumesTable
          rows={data.volumes.filter((v) => match(v.name, ...v.apps, ...v.usedBy))}
          busy={clean.isPending}
          onDelete={(v) =>
            void run(`Delete volume ${v.name}?`, "volumes", {
              ids: [v.name],
              typeToConfirm: v.name.length > 24 ? undefined : v.name,
              message: `${bytes(v.bytes)} of data is deleted for good${v.apps.length ? ` (it belonged to ${v.apps.join(", ")})` : ""}. This cannot be undone.`,
            })
          }
        />
      )}
      {section === "cache" && (
        <CacheTable
          rows={data.buildCache.filter((c) => match(c.description, c.type, c.id))}
          total={data.buildCacheCount}
          busy={clean.isPending}
          onDelete={(c) =>
            void run("Remove this cache entry?", "buildcache", {
              ids: [c.id],
              message: c.description || c.id,
              danger: false,
            })
          }
        />
      )}
    </div>
  );
}

const kindTone = {
  service: "ok",
  platform: "info",
  managed: "info",
  other: "warn",
} as const;

function AppName({ app }: { app: App }) {
  if (app.kind === "service" && app.project) {
    const to: string = serviceUrl({
      project: app.project,
      environment: app.environment ?? "",
      name: app.service ?? "",
    });
    return (
      <Link to={to} className="hover:text-accent font-mono">
        {app.name}
      </Link>
    );
  }
  return (
    <span className="flex items-center gap-1.5">
      <span className="font-mono">{app.name}</span>
      {app.kind !== "service" && (
        <StatusBadge tone={kindTone[app.kind]}>
          {app.kind === "other" ? "outside SynCloud" : app.kind}
        </StatusBadge>
      )}
    </span>
  );
}

function AppsTable({
  data,
  match,
}: {
  data: NodeDocker;
  match: (...s: (string | undefined)[]) => boolean;
}) {
  const rows = data.apps.filter((a) => match(a.name, a.kind));
  const max = Math.max(1, ...data.apps.map((a) => a.bytes));
  return (
    <Panel
      title="What each app consumes"
      flush
      actions={
        <span className="text-faint text-xs">
          images shared by several apps count in full for each
        </span>
      }
    >
      <DataTable
        rows={rows}
        rowKey={(a) => `${a.kind}|${a.name}`}
        empty={<EmptyState icon={Box} title="No containers" />}
        columns={[
          { header: "App", cell: (a) => <AppName app={a} /> },
          {
            header: "Running",
            cell: (a) => (
              <span className={a.running < a.containers ? "text-warn" : ""}>
                {a.running}/{a.containers}
              </span>
            ),
          },
          { header: "Images", cell: (a) => bytes(a.imageBytes) },
          { header: "Volumes", cell: (a) => bytes(a.volumeBytes) },
          {
            header: "Containers + logs",
            cell: (a) => bytes(a.containerBytes),
          },
          {
            header: "Total",
            className: "w-full min-w-48",
            cell: (a) => (
              <div className="flex items-center gap-2">
                <div className="bg-raised flex h-1.5 w-full max-w-56 overflow-hidden rounded-sm">
                  <div className="bg-accent h-full" style={{ width: `${(a.imageBytes / max) * 100}%` }} />
                  <div className="bg-ok h-full" style={{ width: `${(a.volumeBytes / max) * 100}%` }} />
                  <div className="bg-warn h-full" style={{ width: `${(a.containerBytes / max) * 100}%` }} />
                </div>
                <span className="font-mono tabular-nums">{bytes(a.bytes)}</span>
              </div>
            ),
          },
        ]}
      />
    </Panel>
  );
}

function ContainersTable({
  rows,
  busy,
  onDelete,
}: {
  rows: DockerContainer[];
  busy: boolean;
  onDelete: (c: DockerContainer) => void;
}) {
  return (
    <Panel title="Containers" flush>
      <DataTable
        rows={rows}
        rowKey={(c) => c.id}
        empty={<EmptyState icon={Container} title="No containers" />}
        columns={[
          {
            header: "Container",
            cell: (c) => <span className="font-mono">{c.name}</span>,
          },
          { header: "App", cell: (c) => <AppName app={c.app} /> },
          {
            header: "State",
            cell: (c) => (
              <StatusBadge tone={c.state === "running" ? "ok" : "neutral"}>
                {c.state}
              </StatusBadge>
            ),
          },
          { header: "Wrote", cell: (c) => bytes(c.writableBytes) },
          {
            header: "Log",
            cell: (c) => (
              <span className={c.logBytes > 100 << 20 ? "text-warn" : ""}>
                {bytes(c.logBytes)}
              </span>
            ),
          },
          {
            header: "Image",
            className: "w-full",
            cell: (c) => (
              <span className="text-muted block max-w-md truncate font-mono" title={c.image}>
                {c.image}
              </span>
            ),
          },
          {
            header: "Created",
            cell: (c) => <span className="text-muted">{day(c.created)}</span>,
          },
          {
            header: "",
            cell: (c) => (
              <IconButton
                label={c.state === "running" ? "Running: stop it first" : "Remove container"}
                disabled={busy || c.state === "running"}
                onClick={() => onDelete(c)}
              >
                <Trash2 className="size-3.5" />
              </IconButton>
            ),
          },
        ]}
      />
    </Panel>
  );
}

function ImagesTable({
  rows,
  busy,
  onDelete,
}: {
  rows: DockerImage[];
  busy: boolean;
  onDelete: (i: DockerImage) => void;
}) {
  return (
    <Panel title="Images" flush>
      <DataTable
        rows={rows}
        rowKey={(i) => i.id}
        empty={<EmptyState icon={Layers} title="No images" />}
        columns={[
          {
            header: "Image",
            cell: (i) =>
              i.tags.length ? (
                <span className="block max-w-lg truncate font-mono" title={i.tags.join("\n")}>
                  {i.tags[0]}
                  {i.tags.length > 1 && (
                    <span className="text-faint"> +{i.tags.length - 1}</span>
                  )}
                </span>
              ) : (
                <StatusBadge tone="warn">untagged</StatusBadge>
              ),
          },
          {
            header: "ID",
            cell: (i) => (
              <span className="text-muted font-mono">{i.id.replace("sha256:", "").slice(0, 12)}</span>
            ),
          },
          {
            header: "Size",
            cell: (i) => (
              <span title="Removing it frees the part no other image shares">
                {bytes(i.bytes)}
                <span className="text-faint"> · {bytes(Math.max(i.bytes - i.sharedBytes, 0))} own</span>
              </span>
            ),
          },
          {
            header: "Used by",
            className: "w-full",
            cell: (i) =>
              i.containers === 0 ? (
                <StatusBadge tone="warn">unused</StatusBadge>
              ) : (
                <span className="text-muted">
                  {i.apps.join(", ") || `${i.containers} containers`}
                </span>
              ),
          },
          {
            header: "Created",
            cell: (i) => <span className="text-muted">{day(i.created)}</span>,
          },
          {
            header: "",
            cell: (i) => (
              <IconButton
                label={i.containers ? "In use" : "Remove image"}
                disabled={busy || i.containers > 0}
                onClick={() => onDelete(i)}
              >
                <Trash2 className="size-3.5" />
              </IconButton>
            ),
          },
        ]}
      />
    </Panel>
  );
}

function VolumesTable({
  rows,
  busy,
  onDelete,
}: {
  rows: DockerVolume[];
  busy: boolean;
  onDelete: (v: DockerVolume) => void;
}) {
  return (
    <Panel title="Volumes" flush>
      <DataTable
        rows={rows}
        rowKey={(v) => v.name}
        empty={<EmptyState icon={Database} title="No volumes" />}
        columns={[
          {
            header: "Volume",
            cell: (v) => (
              <span className="flex items-center gap-1.5">
                <span
                  className="block max-w-sm truncate font-mono"
                  title={v.name}
                >
                  {v.name}
                </span>
                {v.anonymous && <StatusBadge tone="neutral">anonymous</StatusBadge>}
              </span>
            ),
          },
          { header: "Size", cell: (v) => bytes(v.bytes) },
          {
            header: "Used by",
            className: "w-full",
            cell: (v) =>
              v.refCount === 0 ? (
                <StatusBadge tone="warn">no container</StatusBadge>
              ) : (
                <span className="text-muted" title={v.usedBy.join("\n")}>
                  {v.apps.join(", ") || v.usedBy.join(", ")}
                </span>
              ),
          },
          {
            header: "Created",
            cell: (v) => <span className="text-muted">{day(v.created)}</span>,
          },
          {
            header: "",
            cell: (v) => (
              <IconButton
                label={v.refCount ? "In use" : "Delete volume"}
                disabled={busy || v.refCount > 0}
                onClick={() => onDelete(v)}
              >
                <Trash2 className="size-3.5" />
              </IconButton>
            ),
          },
        ]}
      />
    </Panel>
  );
}

function CacheTable({
  rows,
  total,
  busy,
  onDelete,
}: {
  rows: CacheRecord[];
  total: number;
  busy: boolean;
  onDelete: (c: CacheRecord) => void;
}) {
  return (
    <Panel
      title="Build cache"
      flush
      actions={
        total > rows.length && (
          <span className="text-faint text-xs">
            the largest {rows.length} of {total}
          </span>
        )
      }
    >
      <DataTable
        rows={rows}
        rowKey={(c) => c.id}
        empty={<EmptyState icon={HardDrive} title="No build cache" />}
        columns={[
          {
            header: "Entry",
            className: "w-full",
            cell: (c) => (
              <span className="block max-w-xl truncate" title={c.description}>
                {c.description || c.id}
              </span>
            ),
          },
          {
            header: "Type",
            cell: (c) => <span className="text-muted">{c.type}</span>,
          },
          { header: "Size", cell: (c) => bytes(c.bytes) },
          {
            header: "State",
            cell: (c) =>
              c.inUse ? (
                <StatusBadge tone="info">in use</StatusBadge>
              ) : (
                <StatusBadge tone="neutral">idle</StatusBadge>
              ),
          },
          {
            header: "Last used",
            cell: (c) => <span className="text-muted">{day(c.lastUsed)}</span>,
          },
          {
            header: "",
            cell: (c) => (
              <IconButton
                label="Remove entry"
                disabled={busy || c.inUse}
                onClick={() => onDelete(c)}
              >
                <Trash2 className="size-3.5" />
              </IconButton>
            ),
          },
        ]}
      />
    </Panel>
  );
}
