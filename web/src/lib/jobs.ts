/** A run of a job (§5.11): one-off, scheduled, or a deploy hook. */
export interface JobRun {
  id: string;
  job: string;
  project: string;
  environment: string;
  service?: string;
  trigger: string;
  attempt: number;
  status:
    | "pending"
    | "running"
    | "succeeded"
    | "failed"
    | "timed_out"
    | "cancelled"
    | "skipped";
  node: string;
  exitCode: number | null;
  message: string;
  command: string[];
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
}

export const runTone = {
  pending: "neutral",
  running: "info",
  succeeded: "ok",
  failed: "bad",
  timed_out: "bad",
  cancelled: "neutral",
  skipped: "warn",
} as const;

export function duration(r: JobRun) {
  if (!r.startedAt) return "—";
  const end = r.finishedAt ? new Date(r.finishedAt) : new Date();
  const s = Math.round(
    (end.getTime() - new Date(r.startedAt).getTime()) / 1000,
  );
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}
