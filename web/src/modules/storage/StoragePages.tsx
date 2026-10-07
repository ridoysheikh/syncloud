import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { ChevronRight, Database, Download, FileIcon, Folder, Pencil, Plus, Trash2, Upload } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, IconButton, Input } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { confirmAction } from "@/ui/dialogs";

interface Endpoint {
  id: string;
  name: string;
  url: string;
  region: string;
  accessKeyId: string;
  pathStyle: boolean;
  createdAt: string;
  bindings: number;
}
interface Binding {
  id: string;
  endpoint: string;
  bucket: string;
  prefix: string;
  envPrefix: string;
  project: string;
  environment: string;
  service: string;
}
interface Bucket {
  name: string;
  createdAt: string;
  boundBy: string[];
}
interface S3Object {
  key: string;
  folder: boolean;
  size: number;
  lastModified?: string;
  contentType?: string;
}

const errText = (e: unknown, f: string) => (e instanceof ApiError ? e.message : f);
const enc = encodeURIComponent;
export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1 << 20) return `${(n / 1024).toFixed(1)} KiB`;
  if (n < 1 << 30) return `${(n / (1 << 20)).toFixed(1)} MiB`;
  return `${(n / (1 << 30)).toFixed(2)} GiB`;
}
const bucketAPI = (ep: string, b: string) => `/s3/endpoints/${enc(ep)}/buckets/${enc(b)}`;
export const useEndpoints = () =>
  useQuery({ queryKey: ["s3-endpoints"], queryFn: async () => (await api<{ items: Endpoint[] }>("GET", "/s3/endpoints")).items });

/** Storage › S3 (§16): endpoints, buckets and which services use them. */
export function StoragePage() {
  const { data = [], isLoading, error } = useEndpoints();
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Storage"]}
        title="S3 storage"
        actions={
          <Link to={"/storage/endpoints/new" as string}>
            <Button variant="primary">
              <Plus className="size-3.5" /> Add endpoint
            </Button>
          </Link>
        }
      />
      <Alert tone="info">
        SynCloud has no shared volumes: anything shared or durable goes to S3. Register any S3-compatible provider (AWS S3, Cloudflare R2, Backblaze
        B2, Wasabi, DigitalOcean Spaces…) or a MinIO/Garage service you deploy yourself, then bind buckets to services on their S3 tab: their tasks get
        S3_ENDPOINT, S3_BUCKET, AWS_ACCESS_KEY_ID and the rest as environment variables.
      </Alert>
      {error && <Alert>{errText(error, "Unavailable")}</Alert>}
      <Panel flush>
        <DataTable
          rows={data}
          rowKey={(e) => e.id}
          empty={!isLoading && <EmptyState icon={Database} title="No S3 endpoints">Add one to bind buckets to services and browse them here.</EmptyState>}
          columns={[
            {
              header: "Endpoint",
              cell: (e) => (
                <Link to={`/storage/${e.name}` as string} className="text-accent font-medium hover:underline">
                  {e.name}
                </Link>
              ),
            },
            { header: "URL", cell: (e) => <span className="font-mono">{e.url}</span> },
            { header: "Region", cell: (e) => <span className="text-muted">{e.region || "—"}</span> },
            { header: "Access key", cell: (e) => <span className="text-muted font-mono">{e.accessKeyId}</span> },
            { header: "Addressing", cell: (e) => <span className="text-muted">{e.pathStyle ? "path" : "virtual-host"}</span> },
            { header: "Bindings", className: "w-full", cell: (e) => <span className="text-muted">{e.bindings}</span> },
          ]}
        />
      </Panel>
    </div>
  );
}

/** Add or edit an endpoint, as a full page. */
export function EndpointFormPage() {
  const { endpoint: ref } = useParams({ strict: false }) as { endpoint?: string };
  const isNew = !ref;
  const nav = useNavigate();
  const qc = useQueryClient();
  const cur = useQuery({
    queryKey: ["s3-endpoint", ref],
    queryFn: () => api<{ endpoint: Endpoint }>("GET", `/s3/endpoints/${enc(ref!)}`),
    enabled: !isNew,
  });
  const [f, setF] = useState({ name: "", url: "", region: "", accessKeyId: "", secretAccessKey: "", pathStyle: false });
  const loaded = useRef(false);
  useEffect(() => {
    const e = cur.data?.endpoint;
    if (e && !loaded.current) {
      loaded.current = true;
      setF({ name: e.name, url: e.url, region: e.region, accessKeyId: e.accessKeyId, secretAccessKey: "", pathStyle: e.pathStyle });
    }
  }, [cur.data]);
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) => setF({ ...f, [k]: e.target.value });
  const save = useMutation({
    mutationFn: () => (isNew ? api<Endpoint>("POST", "/s3/endpoints", f) : api<Endpoint>("PUT", `/s3/endpoints/${enc(ref!)}`, f)),
    onSuccess: (e) => {
      void qc.invalidateQueries({ queryKey: ["s3-endpoints"] });
      void qc.invalidateQueries({ queryKey: ["s3-endpoint"] });
      void nav({ to: `/storage/${e.name}` as string });
    },
  });
  return (
    <form
      className={cn("flex flex-col", gap)}
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <PageHeader
        crumbs={["Storage", "S3"]}
        title={isNew ? "Add S3 endpoint" : `Edit ${ref}`}
        actions={
          <>
            <Button variant="ghost" onClick={() => nav({ to: (isNew ? "/storage" : `/storage/${ref}`) as string })}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={save.isPending || !f.name || !f.url || !f.accessKeyId || (isNew && !f.secretAccessKey)}>
              {save.isPending ? "Checking…" : isNew ? "Add endpoint" : "Save"}
            </Button>
          </>
        }
      />
      <Panel title="Endpoint">
        <div className="grid max-w-4xl grid-cols-1 gap-2 sm:grid-cols-2">
          <Field label="Name" hint="how bindings and synctl refer to it">
            <Input value={f.name} onChange={set("name")} placeholder="r2" autoFocus={isNew} />
          </Field>
          <Field label="URL" hint="scheme and host, no bucket or path">
            <Input value={f.url} onChange={set("url")} placeholder="https://s3.eu-central-1.amazonaws.com" className="font-mono" />
          </Field>
          <Field label="Region" hint="auto for R2; empty for MinIO">
            <Input value={f.region} onChange={set("region")} placeholder="eu-central-1" />
          </Field>
          <Field label="Bucket addressing">
            <label className="flex h-8 items-center gap-2 text-xs">
              <input type="checkbox" checked={f.pathStyle} onChange={(e) => setF({ ...f, pathStyle: e.target.checked })} />
              Path-style (MinIO, Garage, most self-hosted servers)
            </label>
          </Field>
          <Field label="Access key ID">
            <Input value={f.accessKeyId} onChange={set("accessKeyId")} className="font-mono" autoComplete="off" />
          </Field>
          <Field label="Secret access key" hint={isNew ? "sealed with the master key; never shown again" : "leave empty to keep the stored one"}>
            <Input type="password" value={f.secretAccessKey} onChange={set("secretAccessKey")} autoComplete="new-password" />
          </Field>
        </div>
        <p className="text-muted mt-2 text-xs">The credentials are checked by listing buckets before the endpoint is saved.</p>
      </Panel>
      {save.error && <Alert>{errText(save.error, "Could not save the endpoint")}</Alert>}
    </form>
  );
}

/** One endpoint: its buckets and the services bound to it. */
export function EndpointPage() {
  const { endpoint } = useParams({ strict: false }) as { endpoint: string };
  const nav = useNavigate();
  const qc = useQueryClient();
  const ep = useQuery({
    queryKey: ["s3-endpoint", endpoint],
    queryFn: () => api<{ endpoint: Endpoint; bindings: Binding[] }>("GET", `/s3/endpoints/${enc(endpoint)}`),
  });
  const buckets = useQuery({
    queryKey: ["s3-buckets", endpoint],
    queryFn: async () => (await api<{ items: Bucket[] }>("GET", `/s3/endpoints/${enc(endpoint)}/buckets`)).items,
    retry: false,
  });
  const [name, setName] = useState("");
  const mb = useMutation({
    mutationFn: () => api("POST", `/s3/endpoints/${enc(endpoint)}/buckets`, { name }),
    onSuccess: () => {
      setName("");
      void qc.invalidateQueries({ queryKey: ["s3-buckets", endpoint] });
    },
  });
  const del = useMutation({
    mutationFn: () => api("DELETE", `/s3/endpoints/${enc(endpoint)}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["s3-endpoints"] });
      void nav({ to: "/storage" as string });
    },
  });
  const e = ep.data?.endpoint;
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Storage", "S3"]}
        title={endpoint}
        actions={
          <>
            <Link to={`/storage/endpoints/${endpoint}/edit` as string}>
              <IconButton label="Edit">
                <Pencil className="size-3.5" />
              </IconButton>
            </Link>
            <IconButton label="Delete" onClick={async () => (await confirmAction(`Delete the S3 endpoint ${endpoint}? Buckets and their data are not touched.`)) && del.mutate()}>
              <Trash2 className="size-3.5" />
            </IconButton>
          </>
        }
      />
      {e && (
        <div className="text-muted flex flex-wrap gap-x-6 text-xs">
          <span className="text-fg font-mono">{e.url}</span>
          <span>region {e.region || "—"}</span>
          <span>{e.pathStyle ? "path-style" : "virtual-host"} addressing</span>
          <span className="font-mono">key {e.accessKeyId}</span>
        </div>
      )}
      {(ep.error || del.error) && <Alert>{errText(ep.error ?? del.error, "Failed")}</Alert>}
      <Panel
        title="Buckets"
        flush
        actions={
          <form
            className="flex items-center gap-1"
            onSubmit={(ev) => {
              ev.preventDefault();
              mb.mutate();
            }}
          >
            <Input value={name} onChange={(ev) => setName(ev.target.value)} placeholder="new-bucket" className="h-7 w-44" />
            <Button type="submit" disabled={!name || mb.isPending}>
              <Plus className="size-3.5" /> Create
            </Button>
          </form>
        }
      >
        {mb.error && <div className="p-2"><Alert>{errText(mb.error, "Could not create the bucket")}</Alert></div>}
        {buckets.error ? (
          <div className="p-2">
            <Alert>{errText(buckets.error, "Cannot list buckets")}</Alert>
          </div>
        ) : (
          <DataTable
            rows={buckets.data ?? []}
            rowKey={(b) => b.name}
            empty={!buckets.isLoading && <EmptyState icon={Database} title="No buckets" />}
            columns={[
              {
                header: "Bucket",
                cell: (b) => (
                  <Link to={`/storage/${endpoint}/${b.name}` as string} className="text-accent font-medium hover:underline">
                    {b.name}
                  </Link>
                ),
              },
              { header: "Created", cell: (b) => <span className="text-muted">{new Date(b.createdAt).toLocaleDateString()}</span> },
              {
                header: "Bound by",
                className: "w-full",
                cell: (b) => <span className="text-muted">{b.boundBy.length ? b.boundBy.join(", ") : "—"}</span>,
              },
            ]}
          />
        )}
      </Panel>
      <Panel title="Service bindings" flush>
        <DataTable
          rows={ep.data?.bindings ?? []}
          rowKey={(b) => b.id}
          empty={<span className="text-faint p-2 text-xs">No service is bound to this endpoint. Bind buckets on a service's S3 tab.</span>}
          columns={[
            {
              header: "Service",
              cell: (b) => (
                <Link
                  to={`/projects/${b.project}/${b.environment}/services/${b.service}?tab=s3` as string}
                  className="text-accent hover:underline"
                >
                  {b.project}/{b.environment}/{b.service}
                </Link>
              ),
            },
            { header: "Bucket", cell: (b) => <span className="font-mono">{b.bucket}</span> },
            { header: "Prefix", cell: (b) => <span className="text-muted font-mono">{b.prefix || "—"}</span> },
            { header: "Variables", className: "w-full", cell: (b) => <span className="text-muted font-mono">{b.envPrefix}S3_*, {b.envPrefix}AWS_*</span> },
          ]}
        />
      </Panel>
    </div>
  );
}

/** The bucket browser: folders, objects, upload, download, delete, usage. */
export function BucketPage() {
  const { endpoint, bucket } = useParams({ strict: false }) as { endpoint: string; bucket: string };
  const qc = useQueryClient();
  const [prefix, setPrefixState] = useState(() => new URLSearchParams(window.location.search).get("prefix") ?? "");
  const setPrefix = (p: string) => {
    setPrefixState(p);
    const u = new URL(window.location.href);
    if (p) u.searchParams.set("prefix", p);
    else u.searchParams.delete("prefix");
    window.history.replaceState(null, "", u);
  };
  const [pages, setPages] = useState<string[]>([""]); // startAfter of each loaded page
  useEffect(() => setPages([""]), [prefix]);
  const base = bucketAPI(endpoint, bucket);
  const list = useQuery({
    queryKey: ["s3-objects", endpoint, bucket, prefix, pages],
    queryFn: async () => {
      const all: S3Object[] = [];
      let truncated = false;
      for (const after of pages) {
        const l = await api<{ objects: S3Object[]; truncated: boolean }>(
          "GET",
          `${base}/objects?prefix=${enc(prefix)}&startAfter=${enc(after)}`,
        );
        all.push(...l.objects);
        truncated = l.truncated;
      }
      return { objects: all, truncated };
    },
  });
  const usage = useMutation({ mutationFn: () => api<{ objects: number; bytes: number; partial: boolean }>("GET", `${base}/usage?prefix=${enc(prefix)}`) });
  const del = useMutation({
    mutationFn: (key: string) => api<{ deleted: number }>("DELETE", `${base}/object?key=${enc(key)}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["s3-objects", endpoint, bucket] }),
  });
  const fileRef = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState("");
  const [uploadErr, setUploadErr] = useState("");
  const upload = async (files: FileList | null) => {
    setUploadErr("");
    for (const f of Array.from(files ?? [])) {
      setUploading(f.name);
      const res = await fetch(`/api/v1${base}/object?key=${enc(prefix + f.name)}`, {
        method: "PUT",
        credentials: "same-origin",
        headers: { "Content-Type": f.type || "application/octet-stream" },
        body: f,
      });
      if (!res.ok) {
        const data = await res.json().catch(() => null);
        setUploadErr(`${f.name}: ${data?.error?.message ?? res.statusText}`);
        break;
      }
    }
    setUploading("");
    if (fileRef.current) fileRef.current.value = "";
    void qc.invalidateQueries({ queryKey: ["s3-objects", endpoint, bucket] });
  };
  const parts = prefix.split("/").filter(Boolean);
  const objects = list.data?.objects ?? [];
  const last = objects[objects.length - 1];
  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader
        crumbs={["Storage", "S3", endpoint]}
        title={bucket}
        actions={
          <>
            <Button variant="ghost" onClick={() => usage.mutate()} disabled={usage.isPending}>
              {usage.isPending ? "Counting…" : "Size"}
            </Button>
            <input ref={fileRef} type="file" multiple className="hidden" onChange={(e) => void upload(e.target.files)} />
            <Button variant="primary" onClick={() => fileRef.current?.click()} disabled={!!uploading}>
              <Upload className="size-3.5" /> {uploading ? `Uploading ${uploading}…` : "Upload"}
            </Button>
          </>
        }
      />
      <div className="flex flex-wrap items-center gap-1 text-xs">
        <Link to={`/storage/${endpoint}` as string} className="text-muted hover:text-fg">
          {endpoint}
        </Link>
        <ChevronRight className="text-faint size-3" />
        <button className={cn(parts.length ? "text-accent hover:underline" : "text-fg font-medium")} onClick={() => setPrefix("")}>
          {bucket}
        </button>
        {parts.map((p, i) => (
          <span key={i} className="flex items-center gap-1">
            <ChevronRight className="text-faint size-3" />
            <button
              className={i === parts.length - 1 ? "text-fg font-medium" : "text-accent hover:underline"}
              onClick={() => setPrefix(parts.slice(0, i + 1).join("/") + "/")}
            >
              {p}
            </button>
          </span>
        ))}
        {usage.data && (
          <span className="text-muted ml-auto">
            {usage.data.partial ? "at least " : ""}
            {usage.data.objects} objects · {bytes(usage.data.bytes)} under {prefix || "the bucket"}
          </span>
        )}
      </div>
      {(list.error || del.error || usage.error || uploadErr) && (
        <Alert>{uploadErr || errText(list.error ?? del.error ?? usage.error, "Failed")}</Alert>
      )}
      <Panel flush>
        <DataTable
          rows={objects}
          rowKey={(o) => o.key}
          empty={!list.isLoading && <EmptyState icon={Folder} title="Empty">Upload files, or let a bound service write here.</EmptyState>}
          columns={[
            {
              header: "Name",
              className: "w-full",
              cell: (o) => {
                const name = o.key.slice(prefix.length);
                return o.folder ? (
                  <button className="text-accent flex items-center gap-1.5 hover:underline" onClick={() => setPrefix(o.key)}>
                    <Folder className="size-3.5" /> {name}
                  </button>
                ) : (
                  <span className="flex items-center gap-1.5 font-mono">
                    <FileIcon className="text-muted size-3.5" /> {name}
                  </span>
                );
              },
            },
            { header: "Size", cell: (o) => <span className="text-muted whitespace-nowrap tabular-nums">{o.folder ? "" : bytes(o.size)}</span> },
            {
              header: "Modified",
              cell: (o) => <span className="text-muted whitespace-nowrap">{o.lastModified ? new Date(o.lastModified).toLocaleString() : ""}</span>,
            },
            { header: "Type", cell: (o) => <span className="text-faint whitespace-nowrap">{o.contentType ?? ""}</span> },
            {
              header: "",
              cell: (o) => (
                <div className="flex justify-end gap-0.5">
                  {!o.folder && (
                    <a href={`/api/v1${base}/object?key=${enc(o.key)}`} aria-label="Download" className="text-muted hover:text-fg p-1">
                      <Download className="size-3.5" />
                    </a>
                  )}
                  <IconButton
                    label="Delete"
                    onClick={async () =>
                      (await confirmAction(o.folder ? `Delete everything under ${o.key}?` : `Delete ${o.key}?`)) && del.mutate(o.key)
                    }
                  >
                    <Trash2 className="size-3.5" />
                  </IconButton>
                </div>
              ),
            },
          ]}
        />
        {list.data?.truncated && last && (
          <div className="p-2">
            <Button variant="ghost" onClick={() => setPages([...pages, last.key])}>
              Load more
            </Button>
          </div>
        )}
      </Panel>
    </div>
  );
}

/** A service's S3 tab: bind buckets; changes roll out as a new revision. */
export function ServiceS3Panel({ path }: { path: string }) {
  const qc = useQueryClient();
  const { data: endpoints = [] } = useEndpoints();
  const cur = useQuery({ queryKey: ["service-s3", path], queryFn: async () => (await api<{ items: Binding[] }>("GET", `${path}/s3`)).items });
  const [rows, setRows] = useState<{ endpoint: string; bucket: string; prefix: string; envPrefix: string }[] | null>(null);
  useEffect(() => {
    if (cur.data && rows === null) setRows(cur.data.map((b) => ({ endpoint: b.endpoint, bucket: b.bucket, prefix: b.prefix, envPrefix: b.envPrefix })));
  }, [cur.data, rows]);
  const save = useMutation({
    mutationFn: () => api<{ revision: number }>("PUT", `${path}/s3`, { bindings: rows ?? [] }),
    onSuccess: () => {
      setRows(null);
      void qc.invalidateQueries({ queryKey: ["service-s3", path] });
      void qc.invalidateQueries({ queryKey: ["services"] });
    },
  });
  const list = rows ?? [];
  const patch = (i: number, p: Partial<(typeof list)[number]>) => setRows(list.map((r, j) => (j === i ? { ...r, ...p } : r)));
  const dirty = rows !== null && JSON.stringify(rows) !== JSON.stringify(cur.data?.map((b) => ({ endpoint: b.endpoint, bucket: b.bucket, prefix: b.prefix, envPrefix: b.envPrefix })));
  const sel = "bg-bg border-line-strong focus:border-accent h-7 rounded-sm border px-2 text-xs outline-none";
  return (
    <Panel
      title="S3 buckets"
      actions={
        <Button variant="primary" disabled={!dirty || save.isPending} onClick={() => save.mutate()}>
          Save and roll out
        </Button>
      }
    >
      <div className="flex flex-col gap-2 text-xs">
        {endpoints.length === 0 && (
          <Alert tone="info">
            No S3 endpoint is registered yet.{" "}
            <Link to={"/storage/endpoints/new" as string} className="underline">
              Add one
            </Link>{" "}
            first.
          </Alert>
        )}
        {list.map((r, i) => (
          <div key={i} className="flex flex-wrap items-end gap-1.5">
            <Field label="Endpoint">
              <select value={r.endpoint} onChange={(e) => patch(i, { endpoint: e.target.value })} className={cn(sel, "w-36")}>
                {endpoints.map((e) => (
                  <option key={e.id} value={e.name}>
                    {e.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Bucket">
              <Input value={r.bucket} onChange={(e) => patch(i, { bucket: e.target.value })} className="h-7 w-44 font-mono" />
            </Field>
            <Field label="Prefix">
              <Input value={r.prefix} onChange={(e) => patch(i, { prefix: e.target.value })} placeholder="optional" className="h-7 w-40 font-mono" />
            </Field>
            <Field label="Variable prefix">
              <Input
                value={r.envPrefix}
                onChange={(e) => patch(i, { envPrefix: e.target.value.toUpperCase() })}
                placeholder={i === 0 ? "none" : "MEDIA_"}
                className="h-7 w-28 font-mono"
              />
            </Field>
            <IconButton label="Remove" onClick={() => setRows(list.filter((_, j) => j !== i))}>
              <Trash2 className="size-3.5" />
            </IconButton>
          </div>
        ))}
        <div>
          <Button
            variant="ghost"
            disabled={endpoints.length === 0}
            onClick={() => setRows([...list, { endpoint: endpoints[0]?.name ?? "", bucket: "", prefix: "", envPrefix: list.length ? `S3${list.length + 1}_` : "" }])}
          >
            <Plus className="size-3.5" /> Bind a bucket
          </Button>
        </div>
        <p className="text-muted">
          Each task gets <span className="font-mono">S3_ENDPOINT, S3_BUCKET, S3_PREFIX, S3_FORCE_PATH_STYLE, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY</span>{" "}
          and <span className="font-mono">AWS_REGION</span> (with the variable prefix, if set). Saving creates a new revision and rolls it out; the
          service's own variables win over these.
        </p>
        {save.error && <Alert>{errText(save.error, "Could not save the bindings")}</Alert>}
      </div>
    </Panel>
  );
}
