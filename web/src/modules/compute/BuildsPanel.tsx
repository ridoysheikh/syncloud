import { useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { GitBranch, Hammer, Rocket, Unplug } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { since } from "@/lib/nodes";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, Input, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { LogsView } from "@/modules/logs/LogsView";

interface GitSource {
  url: string;
  branch: string;
  dockerfile: string;
  context: string;
  hasToken: boolean;
  autoDeploy: boolean;
  pollSeconds: number;
  webhookPath: string;
  webhookSecret: string;
  lastSha: string;
  lastCheckedAt: string | null;
  lastError: string;
}

interface Build {
  id: string;
  sha: string;
  trigger: string;
  status: "queued" | "building" | "succeeded" | "failed";
  image: string;
  runId: string;
  message: string;
  deployed: boolean;
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
}

const buildTone = {
  queued: "neutral",
  building: "info",
  succeeded: "ok",
  failed: "bad",
} as const;

const errText = (e: unknown, fallback: string) =>
  e instanceof ApiError ? e.message : fallback;

/** Git source, builds and build logs of a service (§5.8). */
export function BuildsPanel({ path }: { path: string }) {
  const source = useQuery({
    queryKey: ["git", path],
    queryFn: async () => {
      try {
        return await api<GitSource>("GET", `${path}/git`);
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) return null;
        throw e;
      }
    },
  });
  if (source.isLoading) return null;
  if (!source.data) return <ConnectForm path={path} />;
  return (
    <div className={cn("flex flex-col", gap)}>
      <SourcePanel path={path} src={source.data} />
      <BuildList path={path} />
    </div>
  );
}

function ConnectForm({ path, initial }: { path: string; initial?: GitSource }) {
  const qc = useQueryClient();
  const [url, setUrl] = useState(initial?.url ?? "");
  const [branch, setBranch] = useState(initial?.branch ?? "main");
  const [dockerfile, setDockerfile] = useState(
    initial?.dockerfile ?? "Dockerfile",
  );
  const [context, setContext] = useState(initial?.context ?? "");
  const [token, setToken] = useState("");
  const [autoDeploy, setAutoDeploy] = useState(initial?.autoDeploy ?? true);
  const save = useMutation({
    mutationFn: () =>
      api<GitSource>("PUT", `${path}/git`, {
        url,
        branch,
        dockerfile,
        context,
        token: token || undefined,
        autoDeploy,
        pollSeconds: initial?.pollSeconds,
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["git", path] }),
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (url.trim()) save.mutate();
  };
  return (
    <Panel title="Build from Git">
      <form onSubmit={submit} className="flex max-w-xl flex-col gap-2">
        <p className="text-muted text-xs">
          New commits on the branch are built with BuildKit, pushed to the
          private registry and
          {autoDeploy
            ? " deployed as a new revision"
            : " listed for deploying by hand"}
          .
        </p>
        <Field label="Repository URL">
          <Input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://github.com/acme/api.git"
            className="font-mono"
          />
        </Field>
        <div className="grid grid-cols-1 gap-2 md:grid-cols-3">
          <Field label="Branch">
            <Input
              value={branch}
              onChange={(e) => setBranch(e.target.value)}
              className="font-mono"
            />
          </Field>
          <Field label="Context directory">
            <Input
              value={context}
              onChange={(e) => setContext(e.target.value)}
              placeholder="(root)"
              className="font-mono"
            />
          </Field>
          <Field label="Dockerfile">
            <Input
              value={dockerfile}
              onChange={(e) => setDockerfile(e.target.value)}
              className="font-mono"
            />
          </Field>
        </div>
        <Field
          label="Access token"
          hint={
            initial?.hasToken
              ? "Leave empty to keep the saved token."
              : "Only for private repositories."
          }
        >
          <Input
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            autoComplete="off"
          />
        </Field>
        <label className="flex items-center gap-2 text-xs">
          <input
            type="checkbox"
            checked={autoDeploy}
            onChange={(e) => setAutoDeploy(e.target.checked)}
          />
          Deploy successful builds automatically
        </label>
        {save.error && (
          <Alert>
            {errText(save.error, "Could not connect the repository")}
          </Alert>
        )}
        <div>
          <Button
            type="submit"
            variant="primary"
            disabled={save.isPending || !url.trim()}
          >
            <GitBranch className="size-3.5" />{" "}
            {save.isPending ? "Checking…" : initial ? "Save" : "Connect"}
          </Button>
        </div>
      </form>
    </Panel>
  );
}

function SourcePanel({ path, src }: { path: string; src: GitSource }) {
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const build = useMutation({
    mutationFn: () => api("POST", `${path}/builds`, {}),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["builds", path] }),
  });
  const disconnect = useMutation({
    mutationFn: () => api("DELETE", `${path}/git`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["git", path] }),
  });
  if (editing) return <ConnectForm path={path} initial={src} />;
  return (
    <Panel
      title="Git source"
      actions={
        <>
          <Button
            variant="primary"
            onClick={() => build.mutate()}
            disabled={build.isPending}
          >
            <Hammer className="size-3.5" /> Build now
          </Button>
          <Button variant="ghost" onClick={() => setEditing(true)}>
            Edit
          </Button>
          <Button
            variant="ghost"
            onClick={() =>
              confirm(
                "Stop building this service from Git? Builds and images stay.",
              ) && disconnect.mutate()
            }
          >
            <Unplug className="size-3.5" /> Disconnect
          </Button>
        </>
      }
    >
      <dl className="grid grid-cols-[7rem_1fr] gap-y-1 text-xs">
        <dt className="text-muted">Repository</dt>
        <dd className="font-mono break-all">{src.url}</dd>
        <dt className="text-muted">Branch</dt>
        <dd className="font-mono">
          {src.branch}
          {src.lastSha && (
            <span className="text-faint"> @ {src.lastSha.slice(0, 12)}</span>
          )}
        </dd>
        <dt className="text-muted">Dockerfile</dt>
        <dd className="font-mono">
          {src.context ? `${src.context}/${src.dockerfile}` : src.dockerfile}
        </dd>
        <dt className="text-muted">Deploys</dt>
        <dd>{src.autoDeploy ? "automatically" : "by hand"}</dd>
        <dt className="text-muted">Checked</dt>
        <dd className="text-muted">
          every {src.pollSeconds}s
          {src.lastCheckedAt && `, last ${since(src.lastCheckedAt)}`}
        </dd>
        <dt className="text-muted">Webhook</dt>
        <dd className="font-mono break-all">
          {location.origin}
          {src.webhookPath}
          <div className="text-faint font-sans">
            Content type application/json, secret{" "}
            <span className="font-mono select-all">{src.webhookSecret}</span>.
            Optional: the branch is polled anyway.
          </div>
        </dd>
      </dl>
      {src.lastError && (
        <div className="mt-2">
          <Alert tone="warn">{src.lastError}</Alert>
        </div>
      )}
      {(build.error || disconnect.error) && (
        <div className="mt-2">
          <Alert>
            {errText(build.error ?? disconnect.error, "Request failed")}
          </Alert>
        </div>
      )}
    </Panel>
  );
}

function BuildList({ path }: { path: string }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState<string>();
  const { data = [], isLoading } = useQuery({
    queryKey: ["builds", path],
    queryFn: async () =>
      (await api<{ items: Build[] }>("GET", `${path}/builds`)).items,
    refetchInterval: (q) =>
      q.state.data?.some(
        (b) => b.status === "queued" || b.status === "building",
      )
        ? 2000
        : 10_000,
  });
  const deploy = useMutation({
    mutationFn: (id: string) => api("POST", `/builds/${id}/deploy`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["builds", path] });
      qc.invalidateQueries({ queryKey: ["services"] });
    },
  });
  const shown = data.find((b) => b.id === open);
  return (
    <>
      <Panel title="Builds" flush>
        {deploy.error && (
          <div className="p-2">
            <Alert>{errText(deploy.error, "Deploy failed")}</Alert>
          </div>
        )}
        <DataTable
          rows={data}
          rowKey={(b) => b.id}
          empty={
            !isLoading && (
              <EmptyState icon={Hammer} title="No builds yet">
                The first build starts when the branch is checked.
              </EmptyState>
            )
          }
          columns={[
            {
              header: "Commit",
              cell: (b) => (
                <span className="font-mono">{b.sha.slice(0, 12)}</span>
              ),
            },
            {
              header: "Status",
              cell: (b) => (
                <span className="flex items-center gap-1.5">
                  <StatusBadge tone={buildTone[b.status]}>
                    {b.status}
                  </StatusBadge>
                  {b.deployed && <StatusBadge tone="ok">deployed</StatusBadge>}
                </span>
              ),
            },
            {
              header: "Trigger",
              cell: (b) => <span className="text-muted">{b.trigger}</span>,
            },
            {
              header: "Duration",
              cell: (b) => (
                <span className="text-muted">
                  {b.startedAt && b.finishedAt
                    ? `${Math.round((Date.parse(b.finishedAt) - Date.parse(b.startedAt)) / 1000)}s`
                    : "—"}
                </span>
              ),
            },
            {
              header: "Created",
              cell: (b) => (
                <span className="text-muted">{since(b.createdAt)}</span>
              ),
            },
            {
              header: "Message",
              className: "w-full",
              cell: (b) => (
                <span className="text-muted">{b.message || "—"}</span>
              ),
            },
            {
              header: "",
              cell: (b) => (
                <span className="flex gap-1">
                  {b.runId && (
                    <Button
                      variant="ghost"
                      onClick={() => setOpen(open === b.id ? undefined : b.id)}
                    >
                      {open === b.id ? "Hide log" : "Log"}
                    </Button>
                  )}
                  {b.status === "succeeded" && (
                    <Button
                      variant="ghost"
                      disabled={deploy.isPending}
                      onClick={() =>
                        confirm(
                          `Deploy ${b.sha.slice(0, 12)} as a new revision?`,
                        ) && deploy.mutate(b.id)
                      }
                    >
                      <Rocket className="size-3.5" /> Deploy
                    </Button>
                  )}
                </span>
              ),
            },
          ]}
        />
      </Panel>
      {shown?.runId && (
        <Panel title={`Build log ${shown.sha.slice(0, 12)}`} flush>
          <LogsView filter={{ task: shown.runId }} showSource={false} />
        </Panel>
      )}
    </>
  );
}
