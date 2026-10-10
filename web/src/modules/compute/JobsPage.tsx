import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CalendarClock, Play, Square } from "lucide-react";
import { usePaged } from "@/lib/paged";
import { api, ApiError } from "@/lib/api";
import { subscribe } from "@/lib/stream";
import { since } from "@/lib/nodes";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { Dialog } from "@/ui/Dialog";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, IconButton, StatusBadge } from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { LogsView } from "@/modules/logs/LogsView";
import { duration, runTone, type JobRun } from "@/lib/jobs";

interface Job {
  id: string;
  project: string;
  environment: string;
  name: string;
  spec: {
    kind: string;
    service?: string;
    schedule?: string;
    timezone?: string;
    command?: string[];
  };
  nextRunAt: string | null;
  lastRun: JobRun | null;
}

const jobPath = (j: Job) =>
  `/projects/${j.project}/environments/${j.environment}/jobs/${j.name}`;

/** One-off, scheduled and deploy-hook jobs (§5.11). */
export function JobsPage() {
  const qc = useQueryClient();
  const { data: jobs = [], isLoading } = useQuery({
    queryKey: ["jobs"],
    queryFn: async () => (await api<{ items: Job[] }>("GET", "/jobs")).items,
  });
  useEffect(
    () =>
      subscribe("job.run", () => qc.invalidateQueries({ queryKey: ["jobs"] })),
    [qc],
  );
  const [open, setOpen] = useState<Job | null>(null);
  const run = useMutation({
    mutationFn: (j: Job) => api<JobRun>("POST", `${jobPath(j)}/runs`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["jobs"] }),
  });

  return (
    <div className={cn("flex flex-col", gap)}>
      <PageHeader crumbs={["Compute"]} title="Jobs" />
      {run.error && (
        <Alert>
          {run.error instanceof ApiError
            ? run.error.message
            : "Could not start the job"}
        </Alert>
      )}
      <Panel title="Jobs" flush>
        <DataTable
          rows={jobs}
          rowKey={(j) => j.id}
          empty={
            !isLoading && (
              <EmptyState icon={CalendarClock} title="No jobs">
                Jobs run to completion: migrations, cron jobs and deploy hooks.
                Create them with{" "}
                <code className="font-mono">synctl jobs apply</code>, or run a
                one-off command with{" "}
                <code className="font-mono">synctl run service/NAME -- …</code>.
              </EmptyState>
            )
          }
          columns={[
            {
              header: "Job",
              cell: (j) => (
                <button
                  className="hover:text-accent flex flex-col py-1 text-left"
                  onClick={() => setOpen(j)}
                >
                  <span className="font-medium">{j.name}</span>
                  <span className="text-faint">
                    {j.project} / {j.environment}
                    {j.spec.service && ` · ${j.spec.service}`}
                  </span>
                </button>
              ),
            },
            {
              header: "Kind",
              cell: (j) => (
                <StatusBadge tone="neutral">{j.spec.kind}</StatusBadge>
              ),
            },
            {
              header: "Schedule",
              cell: (j) =>
                j.spec.schedule ? (
                  <span className="font-mono" title={j.spec.timezone || "UTC"}>
                    {j.spec.schedule}
                  </span>
                ) : (
                  <span className="text-faint">—</span>
                ),
            },
            {
              header: "Next run",
              cell: (j) => (
                <span className="text-muted">
                  {j.nextRunAt ? new Date(j.nextRunAt).toLocaleString() : "—"}
                </span>
              ),
            },
            {
              header: "Last run",
              className: "w-full",
              cell: (j) =>
                j.lastRun ? (
                  <span className="flex items-center gap-1.5">
                    <StatusBadge tone={runTone[j.lastRun.status]}>
                      {j.lastRun.status.replace("_", " ")}
                    </StatusBadge>
                    <span className="text-muted">
                      {since(j.lastRun.createdAt)}
                    </span>
                  </span>
                ) : (
                  <span className="text-faint">never</span>
                ),
            },
            {
              header: "",
              cell: (j) => (
                <IconButton label="Run now" onClick={() => run.mutate(j)}>
                  <Play className="size-3.5" />
                </IconButton>
              ),
            },
          ]}
        />
      </Panel>
      {open && <JobDialog job={open} onClose={() => setOpen(null)} />}
    </div>
  );
}

function JobDialog({ job, onClose }: { job: Job; onClose: () => void }) {
  const qc = useQueryClient();
  const runsQ = usePaged<JobRun>(
    ["jobs", job.id, "runs"],
    `${jobPath(job)}/runs`,
    {
      refetchInterval: 3000,
    },
  );
  const runs = runsQ.items;
  const [selected, setSelected] = useState<string | null>(null);
  const cancel = useMutation({
    mutationFn: (id: string) => api("POST", `/runs/${id}/cancel`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["jobs"] }),
  });
  const sel = selected ?? runs.find((r) => r.status !== "skipped")?.id;

  return (
    <Dialog
      open
      onClose={onClose}
      title={`Job ${job.name}`}
      size="xl"
      footer={
        <Button variant="ghost" onClick={onClose}>
          Close
        </Button>
      }
    >
      <div className="flex flex-col gap-2">
        <div className="text-muted text-xs">
          <span className="font-mono">
            {(job.spec.command ?? []).join(" ") || "(task definition command)"}
          </span>
        </div>
        <div className="border-line max-h-60 overflow-auto rounded-sm border">
          <DataTable
            {...runsQ.table}
            rows={runs}
            rowKey={(r) => r.id}
            columns={[
              {
                header: "Run",
                cell: (r) => (
                  <button
                    onClick={() => setSelected(r.id)}
                    className={cn(
                      "font-mono",
                      r.id === sel ? "text-accent" : "hover:text-accent",
                    )}
                  >
                    {r.id}
                  </button>
                ),
              },
              {
                header: "Status",
                cell: (r) => (
                  <StatusBadge tone={runTone[r.status]}>
                    {r.status.replace("_", " ")}
                  </StatusBadge>
                ),
              },
              {
                header: "Trigger",
                cell: (r) => (
                  <span className="text-muted">
                    {r.trigger}
                    {r.attempt > 1 && ` #${r.attempt}`}
                  </span>
                ),
              },
              {
                header: "Exit",
                cell: (r) => (
                  <span className="font-mono">{r.exitCode ?? "—"}</span>
                ),
              },
              {
                header: "Took",
                cell: (r) => <span className="text-muted">{duration(r)}</span>,
              },
              {
                header: "When",
                className: "w-full",
                cell: (r) => (
                  <span className="text-muted" title={r.message}>
                    {since(r.createdAt)} {r.message && `· ${r.message}`}
                  </span>
                ),
              },
              {
                header: "",
                cell: (r) =>
                  (r.status === "running" || r.status === "pending") && (
                    <IconButton
                      label="Cancel run"
                      onClick={() => cancel.mutate(r.id)}
                    >
                      <Square className="size-3.5" />
                    </IconButton>
                  ),
              },
            ]}
          />
        </div>
        {sel && (
          <LogsView key={sel} filter={{ task: sel }} showSource={false} />
        )}
      </div>
    </Dialog>
  );
}
