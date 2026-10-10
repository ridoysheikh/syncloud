import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/lib/api";
import type { ResourceMode, Service, Spec } from "@/lib/workloads";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { ChoiceField, Segmented } from "@/ui/choice";
import { cn } from "@/ui/cn";

/** A task's CPU and memory and whether each is shared or reserved. */
export interface ResourceForm {
  cpu: string;
  memory: string;
  cpuMode: ResourceMode;
  memoryMode: ResourceMode;
}

export const resourceForm = (r: Spec["resources"] = {}): ResourceForm => ({
  cpu: String(r.cpu ?? 0.1),
  memory: String(r.memory ?? 128),
  cpuMode: r.cpuMode === "reserved" ? "reserved" : "shared",
  memoryMode: r.memoryMode === "reserved" ? "reserved" : "shared",
});

/** The resources part of a spec ("shared" is the default and is left out). */
export const resourceSpec = (f: ResourceForm) => ({
  cpu: Number(f.cpu),
  memory: Number(f.memory),
  ...(f.cpuMode === "reserved" ? { cpuMode: "reserved" as const } : {}),
  ...(f.memoryMode === "reserved" ? { memoryMode: "reserved" as const } : {}),
});

export const resourceError = (f: ResourceForm) =>
  !(Number(f.cpu) >= 0.01 && Number(f.cpu) <= 256)
    ? "CPU must be between 0.01 and 256 cores."
    : !(Number(f.memory) >= 4)
      ? "Memory must be at least 4 MiB."
      : null;

const modes = [
  { value: "shared" as const, label: "Shared" },
  { value: "reserved" as const, label: "Reserved" },
];

/** CPU and memory per task, each shared (the default) or reserved. */
export function ResourceFields({
  value: f,
  onChange,
}: {
  value: ResourceForm;
  onChange: (f: ResourceForm) => void;
}) {
  const set = <K extends keyof ResourceForm>(k: K, v: ResourceForm[K]) =>
    onChange({ ...f, [k]: v });
  return (
    <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
      <div className="flex flex-col gap-1.5">
        <Field
          label={f.cpuMode === "reserved" ? "CPU per task (cores reserved)" : "CPU per task (cores expected)"}
        >
          <Input
            type="number"
            step={0.05}
            min={0.01}
            value={f.cpu}
            onChange={(e) => set("cpu", e.target.value)}
          />
        </Field>
        <ChoiceField
          label="CPU"
          hint={
            f.cpuMode === "reserved"
              ? "Set aside on the node: tasks only go where these cores are free, and keep them when the node is busy."
              : "Shares the node's cores; it never stops a task from being placed. When the node is busy, tasks get CPU in proportion to this value."
          }
        >
          <div>
            <Segmented
              label="CPU mode"
              size="sm"
              value={f.cpuMode}
              onChange={(v) => set("cpuMode", v)}
              options={modes}
            />
          </div>
        </ChoiceField>
      </div>
      <div className="flex flex-col gap-1.5">
        <Field
          label={f.memoryMode === "reserved" ? "Memory per task (MiB reserved)" : "Memory per task (MiB expected)"}
          hint="The hard limit is twice this."
        >
          <Input
            type="number"
            min={4}
            value={f.memory}
            onChange={(e) => set("memory", e.target.value)}
          />
        </Field>
        <ChoiceField
          label="Memory"
          hint={
            f.memoryMode === "reserved"
              ? "Set aside on the node, used or not, and kept for the task when memory runs low."
              : "Tasks go to a node with this much memory really free. Unused memory stays free for others."
          }
        >
          <div>
            <Segmented
              label="Memory mode"
              size="sm"
              value={f.memoryMode}
              onChange={(v) => set("memoryMode", v)}
              options={modes}
            />
          </div>
        </ChoiceField>
      </div>
    </div>
  );
}

/** A service's resources, saved as a new revision. */
export function ServiceResourcesPanel({
  path,
  service,
}: {
  path: string;
  service: Service;
}) {
  const qc = useQueryClient();
  const spec = service.spec;
  const [f, setF] = useState(() => resourceForm(spec.resources));
  useEffect(() => {
    setF(resourceForm(spec.resources));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [service.revision]);
  const save = useMutation({
    mutationFn: () => {
      const { sharedEnv: _, ...own } = spec;
      const { cpuMode: _c, memoryMode: _m, ...rest } = spec.resources;
      return api("PUT", path, { ...own, resources: { ...rest, ...resourceSpec(f) } });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["services"] });
      qc.invalidateQueries({ queryKey: ["revisions", path] });
    },
  });
  const dirty = JSON.stringify(f) !== JSON.stringify(resourceForm(spec.resources));
  const invalid = resourceError(f);
  return (
    <Panel title="Resources">
      <div className="flex flex-col gap-3 text-xs">
        <ResourceFields value={f} onChange={setF} />
        {invalid && dirty && <Alert tone="warn">{invalid}</Alert>}
        {save.error && (
          <Alert>
            {save.error instanceof ApiError ? save.error.message : "Saving failed."}
          </Alert>
        )}
        <div className="flex items-center gap-2">
          <Button
            variant="primary"
            disabled={!dirty || !!invalid || save.isPending}
            onClick={() => save.mutate()}
          >
            Save and roll out
          </Button>
          <span className={cn("text-faint", save.isSuccess && !dirty && "text-ok")}>
            {save.isSuccess && !dirty
              ? "Saved: a new revision rolls out."
              : "Saving creates a revision; tasks are replaced with a rolling update."}
          </span>
        </div>
      </div>
    </Panel>
  );
}
