import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/lib/api";
import type { Spec } from "@/lib/workloads";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { ChoiceField, Segmented } from "@/ui/choice";

/*
 * What a service's containers run: the image's own ENTRYPOINT and CMD, a
 * shell line, or exact arguments. One image can then run several services
 * (web, worker, scheduler), each with its own command.
 */

export type CommandMode = "image" | "shell" | "args";

export interface CommandForm {
  mode: CommandMode;
  /** The shell line ("shell"). */
  line: string;
  /** One argument per line ("args"); an empty entrypoint keeps the image's. */
  entrypoint: string;
  command: string;
}

const lines = (s: string) =>
  s
    .split("\n")
    .map((x) => x.trim())
    .filter(Boolean);

export function commandForm(s: Pick<Spec, "entrypoint" | "command"> = {}): CommandForm {
  const ep = s.entrypoint ?? [];
  const cmd = s.command ?? [];
  if (ep.length === 0 && cmd.length === 0)
    return { mode: "image", line: "", entrypoint: "", command: "" };
  if (ep.join(" ") === "sh -c" && cmd.length === 1)
    return { mode: "shell", line: cmd[0] ?? "", entrypoint: "", command: "" };
  return { mode: "args", line: "", entrypoint: ep.join("\n"), command: cmd.join("\n") };
}

/** The entrypoint and command of the spec (both absent for the image's own). */
export function commandSpec(f: CommandForm): Pick<Spec, "entrypoint" | "command"> {
  switch (f.mode) {
    case "shell":
      return { entrypoint: ["sh", "-c"], command: [f.line.trim()] };
    case "args": {
      const ep = lines(f.entrypoint);
      return { ...(ep.length ? { entrypoint: ep } : {}), command: lines(f.command) };
    }
    default:
      return {};
  }
}

export const commandError = (f: CommandForm) =>
  f.mode === "shell" && !f.line.trim()
    ? "Type the command, or choose Image default."
    : f.mode === "args" && lines(f.entrypoint).length + lines(f.command).length === 0
      ? "Give the command's arguments, or choose Image default."
      : null;

const textarea =
  "bg-bg border-line-strong focus:border-line-accent placeholder:text-faint w-full rounded-input border p-2 font-mono text-xs outline-none";

/** The command a service runs, as a choice of mode and its fields. */
export function CommandFields({
  value: f,
  onChange,
}: {
  value: CommandForm;
  onChange: (f: CommandForm) => void;
}) {
  const set = <K extends keyof CommandForm>(k: K, v: CommandForm[K]) =>
    onChange({ ...f, [k]: v });
  return (
    <div className="flex flex-col gap-2">
      <ChoiceField
        label="Command"
        hint={
          f.mode === "image"
            ? "Runs what the image's Dockerfile says (ENTRYPOINT and CMD)."
            : f.mode === "shell"
              ? "Runs this line with sh -c instead of the image's ENTRYPOINT and CMD. Variables like $PORT are expanded. The image needs a shell."
              : "Runs these exact arguments, without a shell. One argument per line."
        }
      >
        <div>
          <Segmented
            label="Command"
            size="sm"
            value={f.mode}
            onChange={(v) => set("mode", v)}
            options={[
              { value: "image", label: "Image default" },
              { value: "shell", label: "Shell command" },
              { value: "args", label: "Arguments" },
            ]}
          />
        </div>
      </ChoiceField>
      {f.mode === "shell" && (
        <Input
          value={f.line}
          onChange={(e) => set("line", e.target.value)}
          placeholder="node worker.js --queue emails"
          aria-label="Shell command"
          className="font-mono"
          spellCheck={false}
          autoComplete="off"
        />
      )}
      {f.mode === "args" && (
        <div className="grid grid-cols-1 gap-2 md:grid-cols-2">
          <Field label="Command (replaces CMD)">
            <textarea
              value={f.command}
              onChange={(e) => set("command", e.target.value)}
              rows={4}
              spellCheck={false}
              placeholder={"worker.js\n--queue\nemails"}
              className={textarea}
            />
          </Field>
          <Field label="Entrypoint (optional, replaces ENTRYPOINT)" hint="Empty keeps the image's.">
            <textarea
              value={f.entrypoint}
              onChange={(e) => set("entrypoint", e.target.value)}
              rows={4}
              spellCheck={false}
              placeholder="node"
              className={textarea}
            />
          </Field>
        </div>
      )}
    </div>
  );
}

/** Service › Deploy › Command: saved as a new revision. */
export function CommandPanel({ path, spec }: { path: string; spec: Spec }) {
  const qc = useQueryClient();
  const initial = commandForm(spec);
  const [f, setF] = useState(initial);
  const dirty = JSON.stringify(commandSpec(f)) !== JSON.stringify(commandSpec(initial));
  const invalid = commandError(f);
  const save = useMutation({
    mutationFn: () => {
      const {
        sharedEnv: _,
        s3: _s3,
        redeployedAt: _r,
        entrypoint: _e,
        command: _c,
        ...own
      } = spec as Spec & { s3?: unknown };
      return api("PUT", path, { ...own, ...commandSpec(f) });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["services"] });
      void qc.invalidateQueries({ queryKey: ["deployments"] });
    },
  });
  return (
    <Panel
      title="Command"
      actions={
        <Button
          variant="primary"
          disabled={!dirty || !!invalid || save.isPending}
          onClick={() => save.mutate()}
        >
          {save.isPending ? "Saving…" : "Save"}
        </Button>
      }
    >
      <div className="flex flex-col gap-2">
        <CommandFields value={f} onChange={setF} />
        <p className="text-faint text-xs">
          Part of the service's spec: saving rolls out a new revision. To run
          one image as several services (a web server and a worker), give each
          service the same image and its own command.
        </p>
        {invalid && dirty && <Alert tone="warn">{invalid}</Alert>}
        {save.error && (
          <Alert>
            {save.error instanceof ApiError ? save.error.message : "Could not save the command"}
          </Alert>
        )}
      </div>
    </Panel>
  );
}
