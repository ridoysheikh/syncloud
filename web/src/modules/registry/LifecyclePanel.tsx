import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Eye, Plus, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { bytes, since } from "@/lib/nodes";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { Alert, Button, IconButton, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { confirmAction } from "@/ui/dialogs";

export interface Rule {
  priority: number;
  description?: string;
  tagPrefix: string;
  keepLast?: number;
  olderThanDays?: number;
}

interface Policy {
  repository: string;
  rules: Rule[];
  updatedAt: string;
}

interface Decision {
  tag: string;
  digest: string;
  sizeBytes: number;
  created: string | null;
  expire: boolean;
  inUse: boolean;
  rule: number;
  reason: string;
}

interface Row {
  priority: string;
  tagPrefix: string;
  mode: "keepLast" | "olderThanDays";
  count: string;
  description: string;
}

const toRow = (r: Rule): Row => ({
  priority: String(r.priority),
  tagPrefix: r.tagPrefix,
  mode: r.keepLast ? "keepLast" : "olderThanDays",
  count: String(r.keepLast || r.olderThanDays || ""),
  description: r.description ?? "",
});

const toRule = (r: Row): Rule => ({
  priority: Number(r.priority),
  tagPrefix: r.tagPrefix.trim(),
  description: r.description.trim() || undefined,
  [r.mode]: Number(r.count),
});

const select = "bg-bg border-line-strong h-7 rounded-input border px-1.5 text-xs";

/** A repository's lifecycle policy with a dry-run preview (§5.10). */
export function LifecyclePanel({ repo }: { repo: string }) {
  const qc = useQueryClient();
  const key = ["registry", "lifecycle", repo];
  const policy = useQuery({
    queryKey: key,
    queryFn: async () => {
      try {
        return await api<Policy>(
          "GET",
          `/registry/lifecycle?repository=${encodeURIComponent(repo)}`,
        );
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return null;
        throw e;
      }
    },
  });
  const [rows, setRows] = useState<Row[]>([]);
  useEffect(() => {
    if (policy.data !== undefined)
      setRows(
        policy.data
          ? policy.data.rules.map(toRow)
          : [
              {
                priority: "1",
                tagPrefix: "",
                mode: "keepLast",
                count: "10",
                description: "",
              },
            ],
      );
  }, [policy.data]);

  const rules = rows.map(toRule);
  const preview = useMutation({
    mutationFn: () =>
      api<{ images: Decision[]; expire: number }>(
        "POST",
        "/registry/lifecycle/preview",
        { repository: repo, rules },
      ),
  });
  const save = useMutation({
    mutationFn: () =>
      api("PUT", `/registry/lifecycle?repository=${encodeURIComponent(repo)}`, {
        rules,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      qc.invalidateQueries({ queryKey: ["registry", "repos"] });
    },
  });
  const remove = useMutation({
    mutationFn: () =>
      api(
        "DELETE",
        `/registry/lifecycle?repository=${encodeURIComponent(repo)}`,
      ),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: key });
      qc.invalidateQueries({ queryKey: ["registry", "repos"] });
      preview.reset();
    },
  });
  const set = (i: number, patch: Partial<Row>) => {
    setRows(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
    preview.reset();
  };
  const err = save.error ?? remove.error ?? preview.error;
  if (policy.isLoading) return null;

  return (
    <div className={cn("flex flex-col", gap)}>
      <Panel
        title={
          policy.data ? "Lifecycle policy · active" : "Lifecycle policy · none"
        }
        actions={
          <>
            <Button
              onClick={() => preview.mutate()}
              disabled={preview.isPending || rows.length === 0}
            >
              <Eye className="size-3.5" /> Preview
            </Button>
            <Button
              variant="primary"
              onClick={() => save.mutate()}
              disabled={save.isPending || rows.length === 0}
            >
              {save.isPending ? "Saving…" : "Save policy"}
            </Button>
            {policy.data && (
              <Button
                variant="ghost"
                onClick={async () =>
                  (await confirmAction(
                    `Remove the lifecycle policy of ${repo}?`,
                  )) && remove.mutate()
                }
              >
                Remove
              </Button>
            )}
          </>
        }
      >
        <div className="flex flex-col gap-2">
          <p className="text-muted text-xs">
            Rules run in priority order (lowest first); each image is decided by
            the first rule whose tag prefix matches it. Images a service runs or
            can roll back to are never deleted. Untagged images are always
            removed by the cleanup that follows.
          </p>
          <div className="text-faint grid grid-cols-[4rem_minmax(0,1fr)_11rem_5rem_minmax(0,1.5fr)_auto] gap-1 text-xs">
            <span>Priority</span>
            <span>Tag prefix</span>
            <span>Action</span>
            <span>Count</span>
            <span>Description</span>
            <span />
          </div>
          {rows.map((r, i) => (
            <div
              key={i}
              className="grid grid-cols-[4rem_minmax(0,1fr)_11rem_5rem_minmax(0,1.5fr)_auto] gap-1"
            >
              <Input
                value={r.priority}
                onChange={(e) => set(i, { priority: e.target.value })}
                className="h-7 font-mono text-xs"
                inputMode="numeric"
              />
              <Input
                value={r.tagPrefix}
                onChange={(e) => set(i, { tagPrefix: e.target.value })}
                placeholder="(any tag)"
                className="h-7 font-mono text-xs"
              />
              <select
                value={r.mode}
                onChange={(e) =>
                  set(i, { mode: e.target.value as Row["mode"] })
                }
                className={select}
              >
                <option value="keepLast">keep the newest</option>
                <option value="olderThanDays">expire after (days)</option>
              </select>
              <Input
                value={r.count}
                onChange={(e) => set(i, { count: e.target.value })}
                className="h-7 font-mono text-xs"
                inputMode="numeric"
              />
              <Input
                value={r.description}
                onChange={(e) => set(i, { description: e.target.value })}
                placeholder="optional"
                className="h-7 text-xs"
              />
              <IconButton
                label="Remove rule"
                onClick={() => setRows(rows.filter((_, j) => j !== i))}
              >
                <Trash2 className="size-3.5" />
              </IconButton>
            </div>
          ))}
          <div>
            <Button
              variant="ghost"
              onClick={() =>
                setRows([
                  ...rows,
                  {
                    priority: String(
                      Math.max(0, ...rows.map((r) => Number(r.priority) || 0)) +
                        1,
                    ),
                    tagPrefix: "",
                    mode: "olderThanDays",
                    count: "30",
                    description: "",
                  },
                ])
              }
            >
              <Plus className="size-3.5" /> Add rule
            </Button>
          </div>
          {err && (
            <Alert>
              {err instanceof ApiError ? err.message : "Request failed"}
            </Alert>
          )}
          {save.isSuccess && !save.isPending && (
            <Alert tone="info">
              Saved. It applies on the next cleanup run.
            </Alert>
          )}
          {policy.data && (
            <span className="text-faint text-xs">
              Last changed {since(policy.data.updatedAt)}
            </span>
          )}
        </div>
      </Panel>
      {preview.data && (
        <Panel
          title={`Preview · ${preview.data.expire} of ${preview.data.images.length} images would be deleted`}
          flush
        >
          <DataTable
            rows={preview.data.images}
            rowKey={(d) => d.tag}
            columns={[
              {
                header: "Tag",
                cell: (d) => <span className="font-mono">{d.tag}</span>,
              },
              {
                header: "Result",
                cell: (d) =>
                  d.expire ? (
                    <StatusBadge tone="bad">expire</StatusBadge>
                  ) : d.inUse ? (
                    <StatusBadge tone="info">in use</StatusBadge>
                  ) : (
                    <StatusBadge tone="ok">keep</StatusBadge>
                  ),
              },
              {
                header: "Rule",
                cell: (d) => (
                  <span className="text-muted">{d.rule || "—"}</span>
                ),
              },
              { header: "Size", cell: (d) => bytes(d.sizeBytes) },
              {
                header: "Created",
                cell: (d) => (
                  <span className="text-muted">
                    {d.created ? since(d.created) : "—"}
                  </span>
                ),
              },
              {
                header: "Reason",
                className: "w-full",
                cell: (d) => <span className="text-muted">{d.reason}</span>,
              },
            ]}
          />
        </Panel>
      )}
    </div>
  );
}
