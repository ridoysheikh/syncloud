import type { Task } from "@/lib/workloads";
import { cn } from "@/ui/cn";

const fill: Record<"running" | "starting" | "missing" | "bad", string> = {
  running: "bg-ok border-ok",
  starting: "bg-info/40 border-info",
  bad: "bg-bad border-bad",
  missing: "border-line-strong bg-transparent",
};

/**
 * One square per wanted task: running, starting, failing, or missing. Shows
 * at a glance how far a service is from its desired count.
 */
export function TaskDots({
  running,
  starting = 0,
  failing = 0,
  desired,
  max = 12,
  className,
}: {
  running: number;
  starting?: number;
  failing?: number;
  desired: number;
  max?: number;
  className?: string;
}) {
  const kinds: (keyof typeof fill)[] = [];
  for (let i = 0; i < running; i++) kinds.push("running");
  for (let i = 0; i < starting; i++) kinds.push("starting");
  for (let i = 0; i < failing; i++) kinds.push("bad");
  while (kinds.length < desired) kinds.push("missing");
  const shown = kinds.slice(0, max);
  const title = `${running} running${starting ? `, ${starting} starting` : ""}${failing ? `, ${failing} failing` : ""} of ${desired} wanted`;
  if (kinds.length === 0)
    return (
      <span className={cn("text-faint text-xs", className)}>no tasks</span>
    );
  return (
    <span
      className={cn("inline-flex items-center gap-0.5", className)}
      title={title}
      aria-label={title}
    >
      {shown.map((k, i) => (
        <span key={i} className={cn("size-2 rounded-[1px] border", fill[k])} />
      ))}
      {kinds.length > max && (
        <span className="text-faint ml-0.5 text-[10px]">
          +{kinds.length - max}
        </span>
      )}
    </span>
  );
}

/** Counts for TaskDots from a list of tasks. */
export function taskCounts(tasks: Task[]) {
  let running = 0,
    starting = 0,
    failing = 0;
  for (const t of tasks) {
    if (t.desired !== "running") continue;
    if (t.state === "running") running++;
    else if (["exited", "failed", "lost"].includes(t.state)) failing++;
    else starting++;
  }
  return { running, starting, failing };
}
