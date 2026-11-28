import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";
import { runTone, type JobRun } from "@/lib/jobs";
import { since } from "@/lib/nodes";
import { Button, IconButton, Input, StatusBadge } from "@/ui/controls";

/* Release commands as an editable, ordered list: the new-service wizard and
   the service's Deploy tab share it. */

export interface CommandRow {
  /** The job's name; unset for a new row. */
  name?: string;
  command: string;
  timeout: string;
  retries: string;
  /** Set while the job's command is not a shell line; editing clears it. */
  raw?: unknown;
  lastRun?: JobRun | null;
}

/** Why the commands can't be saved, or "". */
export function releaseCommandsError(...lists: CommandRow[][]): string {
  const rows = lists.flat();
  if (rows.some((r) => !r.command.trim()))
    return "Fill in or remove the empty command.";
  if (
    rows.some(
      (r) =>
        (r.timeout && !(Number(r.timeout) >= 1)) ||
        (r.retries && !(Number(r.retries) >= 0 && Number(r.retries) <= 10)),
    )
  )
    return "Timeouts are seconds (at least 1); retries are 0–10.";
  return "";
}

/** The commands run before and after each deployment. */
export function ReleaseCommandLists({
  pre,
  post,
  onPre,
  onPost,
}: {
  pre: CommandRow[];
  post: CommandRow[];
  onPre: (rows: CommandRow[]) => void;
  onPost: (rows: CommandRow[]) => void;
}) {
  return (
    <>
      <CommandList
        title="Before each deployment"
        hint="Run one at a time, in order, in the new revision's image with its variables, before it takes traffic: database migrations and similar. If one fails, the deployment stops and the running revision stays. Rollbacks skip them unless you ask."
        placeholder="npm run migrate"
        rows={pre}
        onChange={onPre}
      />
      <CommandList
        title="After each successful deployment"
        hint="Run in order once every task of the new revision serves traffic: cache purges, smoke tests, notifications. A failure stops the rest but does not undo the deployment."
        placeholder="./scripts/purge-cache.sh"
        rows={post}
        onChange={onPost}
      />
    </>
  );
}

function CommandList({
  title,
  hint,
  placeholder,
  rows,
  onChange,
}: {
  title: string;
  hint: string;
  placeholder: string;
  rows: CommandRow[];
  onChange: (rows: CommandRow[]) => void;
}) {
  const update = (i: number, r: Partial<CommandRow>) =>
    onChange(rows.map((x, j) => (j === i ? { ...x, ...r } : x)));
  const move = (i: number, d: number) => {
    const next = rows.slice();
    const [r] = next.splice(i, 1);
    if (r) next.splice(i + d, 0, r);
    onChange(next);
  };
  return (
    <section className="flex flex-col gap-1.5">
      <h3 className="text-xs font-medium">{title}</h3>
      <p className="text-faint text-xs">{hint}</p>
      {rows.length === 0 && <p className="text-muted text-xs">No commands.</p>}
      <ol className="flex flex-col gap-1.5">
        {rows.map((r, i) => (
          <li
            key={r.name ?? `new-${i}`}
            className="border-line flex flex-col gap-1.5 rounded-md border p-1.5 sm:flex-row sm:items-start"
          >
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              <label className="flex flex-col gap-0.5">
                <span className="text-faint text-xs">Command {i + 1}</span>
                <Input
                  value={r.command}
                  onChange={(e) =>
                    update(i, { command: e.target.value, raw: undefined })
                  }
                  placeholder={placeholder}
                  className="font-mono"
                  spellCheck={false}
                />
              </label>
              <div className="text-faint flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
                {r.name && <span className="font-mono">job {r.name}</span>}
                {!!r.raw && <span>exec form; editing makes it a shell line</span>}
                {r.lastRun && (
                  <span className="flex items-center gap-1">
                    last run
                    <StatusBadge tone={runTone[r.lastRun.status]}>
                      {r.lastRun.status.replace("_", " ")}
                    </StatusBadge>
                    {since(r.lastRun.createdAt)}
                  </span>
                )}
              </div>
            </div>
            <div className="flex items-end gap-1">
              <label className="flex flex-col gap-0.5">
                <span className="text-faint text-xs">Timeout (s)</span>
                <Input
                  value={r.timeout}
                  onChange={(e) => update(i, { timeout: e.target.value })}
                  placeholder="3600"
                  inputMode="numeric"
                  className="h-8 w-20"
                />
              </label>
              <label className="flex flex-col gap-0.5">
                <span className="text-faint text-xs">Retries</span>
                <Input
                  value={r.retries}
                  onChange={(e) => update(i, { retries: e.target.value })}
                  placeholder="0"
                  inputMode="numeric"
                  className="h-8 w-16"
                />
              </label>
              <IconButton
                label="Move up"
                disabled={i === 0}
                onClick={() => move(i, -1)}
              >
                <ArrowUp className="size-3.5" />
              </IconButton>
              <IconButton
                label="Move down"
                disabled={i === rows.length - 1}
                onClick={() => move(i, 1)}
              >
                <ArrowDown className="size-3.5" />
              </IconButton>
              <IconButton
                label="Remove"
                onClick={() => onChange(rows.filter((_, j) => j !== i))}
              >
                <Trash2 className="size-3.5" />
              </IconButton>
            </div>
          </li>
        ))}
      </ol>
      <div>
        <Button
          variant="ghost"
          onClick={() =>
            onChange([...rows, { command: "", timeout: "", retries: "" }])
          }
        >
          <Plus className="size-3.5" /> Add command
        </Button>
      </div>
    </section>
  );
}
