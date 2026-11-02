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
import { Combobox, Select } from "@/ui/select";
import { groupImages, isIdTag, shortRef, type Image } from "./images";

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
  tags: string[];
  names: string[];
  digest: string;
  sizeBytes: number;
  created: string | null;
  pushedAt: string | null;
  expire: boolean;
  inUse: boolean;
  wouldExpire: boolean;
  cache: boolean;
  rule: number;
  reason: string;
}

interface Preview {
  images: Decision[];
  expire: number;
  freesBytes: number;
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


/** The name of the default policy, applied to repositories without their own. */
export const DEFAULT_POLICY = "*";

/**
 * A repository's lifecycle policy with a dry-run preview (§5.10). With repo
 * "*" it edits the default policy, which applies to every repository that
 * has none of its own; the preview then tries it on a repository you pick.
 */
export function LifecyclePanel({ repo }: { repo: string }) {
  const qc = useQueryClient();
  const isDefault = repo === DEFAULT_POLICY;
  const key = ["registry", "lifecycle", repo];
  const repos = useQuery({
    queryKey: ["registry", "repos"],
    queryFn: async () =>
      (await api<{ items: { name: string }[] }>("GET", "/registry/repositories"))
        .items,
    enabled: isDefault,
  });
  const [tryOn, setTryOn] = useState("");
  const target = isDefault ? tryOn || repos.data?.[0]?.name || "" : repo;
  // Without its own policy a repository follows the default, if there is one.
  const fallback = useQuery({
    queryKey: ["registry", "lifecycle", DEFAULT_POLICY],
    queryFn: async () => {
      try {
        return await api<Policy>(
          "GET",
          `/registry/lifecycle?repository=${encodeURIComponent(DEFAULT_POLICY)}`,
        );
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return null;
        throw e;
      }
    },
    enabled: !isDefault,
  });
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
  // Names the repository's images answer to, to complete the prefix field:
  // their tags, and the latest-<branch> tag the builds behind them pushed.
  const imgs = useQuery({
    queryKey: ["registry", "images", target],
    queryFn: async () =>
      (
        await api<{ items: Image[] }>(
          "GET",
          `/registry/images?repository=${encodeURIComponent(target)}`,
        )
      ).items,
    enabled: !!target,
  });
  const suggestions = [
    ...new Set(
      groupImages(imgs.data ?? []).flatMap((g) => [
        ...g.tags.filter((t) => !isIdTag(t)),
        ...(g.builtFrom
          ? [
              g.builtFrom.ref.startsWith("refs/tags/")
                ? shortRef(g.builtFrom.ref)
                : `latest-${shortRef(g.builtFrom.ref).replace(/[^A-Za-z0-9_.-]/g, "-")}`,
            ]
          : []),
      ]),
    ),
  ];
  const preview = useMutation({
    mutationFn: () =>
      api<Preview>("POST", "/registry/lifecycle/preview", {
        repository: target,
        rules,
      }),
  });
  const addRule = (r: Partial<Row>) =>
    setRows([
      ...rows,
      {
        priority: String(
          Math.max(0, ...rows.map((x) => Number(x.priority) || 0)) + 10,
        ),
        tagPrefix: "",
        mode: "olderThanDays",
        count: "30",
        description: "",
        ...r,
      },
    ]);
  const save = useMutation({
    mutationFn: () =>
      api("PUT", `/registry/lifecycle?repository=${encodeURIComponent(repo)}`, {
        rules,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["registry", "lifecycle"] });
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
      qc.invalidateQueries({ queryKey: ["registry", "lifecycle"] });
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
          (isDefault ? "Default policy" : "Lifecycle policy") +
          (policy.data ? " · active" : " · not saved")
        }
        actions={
          <>
            <Button
              onClick={() => preview.mutate()}
              disabled={preview.isPending || rows.length === 0 || !target}
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
                    isDefault
                      ? "Remove the default policy? Repositories without a policy of their own are no longer cleaned."
                      : `Remove the lifecycle policy of ${repo}?`,
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
          {!policy.data && !isDefault && (
            <Alert tone={fallback.data ? "info" : "warn"}>
              {fallback.data
                ? "This repository has no policy of its own, so the default policy applies (keeping the newest images as set there). Save a policy here to override it."
                : "No policy is saved for this repository, so nothing is ever deleted from it. The rule below is only a suggestion: it takes effect when you Save policy."}
            </Alert>
          )}
          {!policy.data && isDefault && (
            <Alert tone="warn">
              No default policy is saved. Repositories without a policy of
              their own are never cleaned. The rule below is only a
              suggestion: it takes effect when you Save policy.
            </Alert>
          )}
          {isDefault && (
            <div className="flex items-center gap-2 text-xs">
              <span className="text-muted">Preview on</span>
              <div className="w-64">
                <Select value={target} onChange={setTryOn} size="sm">
                  {(repos.data ?? []).map((r) => (
                    <option key={r.name} value={r.name}>
                      {r.name}
                    </option>
                  ))}
                </Select>
              </div>
              <span className="text-faint">
                Applies to every repository without its own policy.
              </span>
            </div>
          )}
          <p className="text-muted text-xs">
            Rules run in priority order (lowest first). A rule judges whole
            images: an image matches when <em>any</em> of its tags, or the
            branch tag of the build it came from (<code>latest-main</code>),
            starts with the prefix, and it counts once however many tags it
            has. Age counts from the last push. Images a service runs or can
            roll back to, and the builders' cache, are never deleted; an image
            no rule matches is kept, so end with a catch-all rule (empty
            prefix). Untagged images are removed by the cleanup that follows.
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
              <Combobox
                value={r.tagPrefix}
                onChange={(v) => set(i, { tagPrefix: v })}
                suggestions={suggestions}
                placeholder="(every image)"
                className="h-7 font-mono text-xs"
              />
              <Select
                value={r.mode}
                onChange={(v) =>
                  set(i, { mode: v as Row["mode"] })
                }
                size="sm"
              >
                <option value="keepLast">keep the newest</option>
                <option value="olderThanDays">expire days after push</option>
              </Select>
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
          <div className="flex flex-wrap items-center gap-1">
            <Button variant="ghost" onClick={() => addRule({})}>
              <Plus className="size-3.5" /> Add rule
            </Button>
            <span className="text-faint text-xs">Quick rules:</span>
            <Button
              onClick={() =>
                addRule({ mode: "keepLast", count: "10", description: "Keep the newest 10 images" })
              }
            >
              Keep newest 10
            </Button>
            <Button
              onClick={() =>
                addRule({ mode: "olderThanDays", count: "30", description: "Expire after 30 days" })
              }
            >
              Expire after 30 days
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
          title={`Preview · ${preview.data.expire} of ${preview.data.images.length} images would be deleted${
            preview.data.freesBytes
              ? `, freeing about ${bytes(preview.data.freesBytes)}`
              : ""
          }`}
          flush
        >
          <DataTable
            rows={preview.data.images}
            rowKey={(d) => d.digest || d.tag}
            columns={[
              {
                header: "Image",
                cell: (d) => (
                  <span
                    className="flex items-center gap-1 font-mono"
                    title={d.tags.join("\n")}
                  >
                    {d.tags.slice(0, 3).join(", ")}
                    {d.tags.length > 3 && (
                      <span className="text-faint">+{d.tags.length - 3}</span>
                    )}
                  </span>
                ),
              },
              {
                header: "Result",
                cell: (d) =>
                  d.expire ? (
                    <StatusBadge tone="bad">delete</StatusBadge>
                  ) : d.wouldExpire || d.inUse ? (
                    <StatusBadge tone="info">kept: in use</StatusBadge>
                  ) : d.cache ? (
                    <StatusBadge tone="neutral">build cache</StatusBadge>
                  ) : d.rule === 0 ? (
                    <StatusBadge tone="warn">no rule</StatusBadge>
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
                header: "Pushed",
                cell: (d) => (
                  <span className="text-muted">
                    {d.pushedAt || d.created
                      ? since(d.pushedAt ?? d.created)
                      : "—"}
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
