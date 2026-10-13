import { useCallback, useId, useState, type FormEvent } from "react";
import { Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Cloud, HardDrive } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { useStreamTopic } from "@/lib/stream";
import { bytes, since } from "@/lib/nodes";
import { useS3Buckets, useS3Endpoints } from "@/lib/pg";
import { Panel } from "@/ui/Panel";
import { Dialog } from "@/ui/Dialog";
import { ChoiceCards } from "@/ui/choice";
import { Select } from "@/ui/select";
import { Alert, Button, Field, Input, StatusBadge } from "@/ui/controls";
import { confirmAction } from "@/ui/dialogs";

interface Target {
  driver: "disk" | "s3";
  endpoint?: string;
  bucket?: string;
  prefix?: string;
}

interface Switch {
  from: Target;
  to: Target;
  state: "running" | "succeeded" | "failed";
  step: string;
  progress: {
    repositories: number;
    repositoriesDone: number;
    images: number;
    imagesDone: number;
    blobs: number;
    skipped: number;
    bytes: number;
    current?: string;
  };
  message?: string;
  kept?: string;
  freedBytes?: number;
  startedAt: string;
  endedAt?: string | null;
}

interface Storage extends Target {
  endpointName?: string;
  endpointUrl?: string;
  switch?: Switch;
}

function where(t: Target, endpointName?: string) {
  if (t.driver !== "s3") return "Controller disk";
  return `s3://${t.bucket}${t.prefix ? "/" + t.prefix : ""}${endpointName ? ` (${endpointName})` : ""}`;
}

/** Where the registry keeps its images, and switching it (Phase 19). */
export function StoragePanel() {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const { data, error } = useQuery({
    queryKey: ["registry", "storage"],
    queryFn: () => api<Storage>("GET", "/registry/storage"),
    refetchInterval: (q) =>
      q.state.data?.switch?.state === "running" ? 3000 : false,
    retry: false,
  });
  const onEvent = useCallback(
    (e: { data: Storage }) => {
      qc.setQueryData(["registry", "storage"], e.data);
      if (e.data.switch?.state !== "running")
        void qc.invalidateQueries({ queryKey: ["registry", "gc"] });
    },
    [qc],
  );
  useStreamTopic("registry.storage", onEvent);
  if (error || !data) return null; // switching needs the registry system task
  const sw = data.switch;
  const running = sw?.state === "running";
  return (
    <Panel
      title="Storage"
      actions={
        <Button onClick={() => setOpen(true)} disabled={running}>
          {running ? "Switching…" : "Change"}
        </Button>
      }
    >
      <div className="flex flex-col gap-2 text-xs">
        <div className="flex min-w-0 items-center gap-2">
          {data.driver === "s3" ? (
            <Cloud className="text-muted size-4 shrink-0" />
          ) : (
            <HardDrive className="text-muted size-4 shrink-0" />
          )}
          <span
            className="min-w-0 truncate font-mono"
            title={where(data, data.endpointName)}
          >
            {where(data, data.endpointName)}
          </span>
          {data.endpointUrl && (
            <span
              className="text-muted min-w-0 truncate"
              title={data.endpointUrl}
            >
              {data.endpointUrl}
            </span>
          )}
        </div>
        <p className="text-muted">
          {data.driver === "s3"
            ? "Images live in the bucket and nodes download layers from it directly; the controller's disk holds none."
            : "Images live on the controller's disk (the syncloud-registry volume). Move them to S3 to stop the disk growing with images."}
        </p>
        {sw && <SwitchStatus sw={sw} />}
      </div>
      <SwitchDialog open={open} onClose={() => setOpen(false)} current={data} />
    </Panel>
  );
}

function SwitchStatus({ sw }: { sw: Switch }) {
  const p = sw.progress;
  const pct = p.images > 0 ? (p.imagesDone / p.images) * 100 : 0;
  if (sw.state === "running")
    return (
      <div className="border-line flex flex-col gap-1.5 rounded-sm border p-2">
        <div className="flex items-center gap-2">
          <StatusBadge tone="info">switching</StatusBadge>
          <span className="min-w-0 truncate">
            {where(sw.from)} → {where(sw.to)}
          </span>
        </div>
        <div className="bg-raised h-1 overflow-hidden rounded-sm">
          <div
            className="bg-accent h-full rounded-sm"
            style={{ width: `${pct}%` }}
          />
        </div>
        <div className="text-muted flex flex-wrap gap-x-3">
          <span>{sw.step}</span>
          <span>
            {p.imagesDone}/{p.images} images
          </span>
          <span>{bytes(p.bytes)} copied</span>
          {p.current && (
            <span className="min-w-0 truncate font-mono" title={p.current}>
              {p.current}
            </span>
          )}
        </div>
        <p className="text-muted">
          Pushes are refused until the switch finishes; pulls keep working.
        </p>
      </div>
    );
  return (
    <div className="flex flex-col gap-1">
      <div className="text-muted flex flex-wrap items-center gap-x-2">
        <StatusBadge tone={sw.state === "succeeded" ? "ok" : "bad"}>
          {sw.state === "succeeded" ? "switched" : "switch failed"}
        </StatusBadge>
        <span className="min-w-0 truncate">
          {where(sw.from)} → {where(sw.to)}
        </span>
        <span>{since(sw.endedAt ?? sw.startedAt)}</span>
        {sw.state === "succeeded" && (
          <span>
            {p.images} images, {bytes(p.bytes)} copied
            {sw.freedBytes ? `, ${bytes(sw.freedBytes)} of disk freed` : ""}
          </span>
        )}
      </div>
      {sw.message && <Alert>{sw.message}</Alert>}
      {sw.kept && (
        <Alert tone="info">
          The old images were kept at{" "}
          <span className="font-mono break-all">{sw.kept}</span>. Delete them
          from the{" "}
          <Link
            to={
              `/storage/${encodeURIComponent(sw.from.endpoint ?? "")}` as string
            }
            className="underline"
          >
            bucket browser
          </Link>{" "}
          when you no longer need them.
        </Alert>
      )}
    </div>
  );
}

function SwitchDialog({
  open,
  onClose,
  current,
}: {
  open: boolean;
  onClose: () => void;
  current: Storage;
}) {
  const qc = useQueryClient();
  const formId = useId();
  const [t, setT] = useState<Target>(() =>
    current.driver === "s3"
      ? { driver: "disk" }
      : { driver: "s3", endpoint: "", bucket: "", prefix: "" },
  );
  const endpoints = useS3Endpoints();
  const buckets = useS3Buckets(t.driver === "s3" ? (t.endpoint ?? "") : "");
  const start = useMutation({
    mutationFn: (body: Target) =>
      api<Storage>("PUT", "/registry/storage", body),
    onSuccess: (st) => {
      qc.setQueryData(["registry", "storage"], st);
      onClose();
    },
  });
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const leavingDisk = current.driver === "disk";
    const ok = await confirmAction(
      `Switch the registry to ${where(t)}? Every image is copied first; pushes are refused until it finishes, pulls keep working. ` +
        (leavingDisk
          ? "Afterwards the images are deleted from the controller's disk."
          : "The old images stay in the bucket until you delete them."),
    );
    if (ok) start.mutate(t);
  };
  const noEndpoints = endpoints.data && endpoints.data.length === 0;
  const ready = t.driver === "disk" || (!!t.endpoint && !!t.bucket);
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="Registry storage"
      description={`Now: ${where(current, current.endpointName)}`}
      size="lg"
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form={formId}
            variant="primary"
            disabled={!ready || start.isPending}
          >
            Switch and copy images
          </Button>
        </>
      }
    >
      <form
        id={formId}
        onSubmit={submit}
        className="flex flex-col gap-3 text-xs"
      >
        <ChoiceCards
          label="Storage"
          value={t.driver}
          onChange={(d) =>
            setT(
              d === "s3"
                ? { driver: "s3", endpoint: "", bucket: "", prefix: "" }
                : { driver: "disk" },
            )
          }
          options={[
            {
              value: "disk",
              title: "Controller disk",
              icon: HardDrive,
              description:
                "The syncloud-registry volume on the controller. Grows with every image.",
            },
            {
              value: "s3",
              title: "S3 bucket",
              icon: Cloud,
              description:
                "Any registered S3 endpoint. The controller's disk holds no images.",
            },
          ]}
        />
        {t.driver === "s3" && (
          <>
            {noEndpoints ? (
              <Alert tone="warn">
                No S3 endpoint is registered yet. Add one under{" "}
                <Link to={"/storage" as string} className="underline">
                  Storage
                </Link>{" "}
                first.
              </Alert>
            ) : (
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <Field label="S3 endpoint">
                  <Select
                    value={t.endpoint ?? ""}
                    onChange={(v) => setT({ ...t, endpoint: v, bucket: "" })}
                    className="w-full"
                  >
                    <option value="">Choose…</option>
                    {(endpoints.data ?? []).map((e) => (
                      <option key={e.id} value={e.id}>
                        {e.name} — {e.url}
                      </option>
                    ))}
                  </Select>
                </Field>
                {t.endpoint && (
                  <Field
                    label="Bucket"
                    hint="Must exist; a bucket used only by the registry is simplest."
                  >
                    <Select
                      value={t.bucket ?? ""}
                      onChange={(v) => setT({ ...t, bucket: v })}
                      className="w-full"
                    >
                      <option value="">Choose…</option>
                      {(buckets.data ?? []).map((b) => (
                        <option key={b.name} value={b.name}>
                          {b.name}
                        </option>
                      ))}
                    </Select>
                  </Field>
                )}
                {t.endpoint && (
                  <Field
                    label="Prefix (optional)"
                    hint="Keys go under it; empty uses the bucket root."
                  >
                    <Input
                      value={t.prefix ?? ""}
                      onChange={(e) => setT({ ...t, prefix: e.target.value })}
                      placeholder="registry"
                      className="w-full font-mono"
                    />
                  </Field>
                )}
              </div>
            )}
            <Alert tone="warn">
              Nodes download image layers from the bucket directly (presigned
              URLs), so every node must reach the endpoint, with a valid
              certificate on HTTPS.
            </Alert>
          </>
        )}
        <p className="text-muted">
          Images are copied through a temporary registry and checked before the
          registry switches. Pushes are refused while they copy; pulls keep
          working. If anything fails, the registry stays where it is.
          {current.driver === "disk"
            ? " After the switch, the images are deleted from the controller's disk."
            : " The old images stay in the bucket until you delete them."}
        </p>
        {start.error && (
          <Alert>
            {start.error instanceof ApiError
              ? start.error.message
              : "Could not start the switch"}
          </Alert>
        )}
      </form>
    </Dialog>
  );
}
