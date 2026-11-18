import { useEffect, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "@/lib/api";
import type { ResourceMode, Service, Spec } from "@/lib/workloads";
import { Panel } from "@/ui/Panel";
import { Alert, Button, Field, Input } from "@/ui/controls";
import { ChoiceField, Segmented } from "@/ui/choice";
import { cn } from "@/ui/cn";

/**
 * A task's CPU and memory, each shared (the default) or reserved (Phase 27).
 * Shared asks for no amount: the task shares the node, and only an optional
 * limit caps it. Reserved sets the amount aside on the node.
 */
export interface ResourceForm {
  cpuMode: ResourceMode;
  memoryMode: ResourceMode;
  /** Reserved: the cores set aside. Shared: the CPU weight kept from the spec (hidden). */
  cpu: string;
  /** Reserved: the MiB set aside. Shared: what placement keeps free (hidden). */
  memory: string;
  /** Empty: no limit. */
  cpuLimit: string;
  /** Empty: no limit. */
  memoryLimit: string;
}

const DEFAULT_CPU = "0.1";
const DEFAULT_MEMORY = "128";

/** The form for a spec's resources; a new service (no spec) has no limits. */
export const resourceForm = (r: Spec["resources"] = {}): ResourceForm => ({
  cpuMode: r.cpuMode === "reserved" ? "reserved" : "shared",
  memoryMode: r.memoryMode === "reserved" ? "reserved" : "shared",
  cpu: String(r.cpu ?? DEFAULT_CPU),
  memory: String(r.memory ?? DEFAULT_MEMORY),
  cpuLimit: r.cpuLimit ? String(r.cpuLimit) : "",
  // -1 is no limit; an existing spec without one has the API's default,
  // twice its memory, which stays unless the user changes it.
  memoryLimit:
    r.memoryLimit === -1
      ? ""
      : r.memoryLimit
        ? String(r.memoryLimit)
        : r.memory != null
          ? String(2 * r.memory)
          : "",
});

/** The resources part of a spec ("shared" is the default and is left out). */
export const resourceSpec = (f: ResourceForm) => {
  const limit = f.memoryLimit ? Number(f.memoryLimit) : -1; // -1: no limit
  let memory = Number(f.memory);
  // A shared task's hidden placement amount never exceeds its limit.
  if (f.memoryMode === "shared" && limit > 0 && limit < memory) memory = limit;
  return {
    cpu: Number(f.cpu),
    memory,
    ...(f.cpuMode === "reserved" ? { cpuMode: "reserved" as const } : {}),
    ...(f.memoryMode === "reserved" ? { memoryMode: "reserved" as const } : {}),
    ...(f.cpuLimit ? { cpuLimit: Number(f.cpuLimit) } : {}),
    memoryLimit: limit,
  };
};

export const resourceError = (f: ResourceForm) =>
  f.cpuMode === "reserved" && !(Number(f.cpu) >= 0.01 && Number(f.cpu) <= 256)
    ? "Reserved CPU must be between 0.01 and 256 cores."
    : f.memoryMode === "reserved" && !(Number(f.memory) >= 4)
      ? "Reserved memory must be at least 4 MiB."
      : f.cpuLimit && !(Number(f.cpuLimit) >= 0.01 && Number(f.cpuLimit) <= 256)
        ? "The CPU limit must be between 0.01 and 256 cores."
        : f.memoryLimit && !(Number(f.memoryLimit) >= 4)
          ? "The memory limit must be at least 4 MiB."
          : f.memoryLimit && f.memoryMode === "reserved" && Number(f.memoryLimit) < Number(f.memory)
            ? "The memory limit can't be below the reserved memory."
            : null;

/** Resources in words, for reviews ("shared CPU, memory up to 512 MiB"). */
export const resourceSummary = (f: ResourceForm) => {
  const cpu =
    f.cpuMode === "reserved" ? `${f.cpu} CPU reserved` : "shared CPU";
  const cpuCap = f.cpuLimit ? ` (at most ${f.cpuLimit})` : "";
  const mem =
    f.memoryMode === "reserved" ? `${f.memory} MiB reserved` : "shared memory";
  const memCap = f.memoryLimit ? ` (at most ${f.memoryLimit} MiB)` : ", no limit";
  return `${cpu}${cpuCap}, ${mem}${memCap}`;
};

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
  const set = (p: Partial<ResourceForm>) => onChange({ ...f, ...p });
  const num = (v: string) => v.replace(/[^0-9.]/g, "");
  return (
    <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
      <div className="border-line flex flex-col gap-2 rounded-md border p-2">
        <ChoiceField
          label="CPU"
          hint={
            f.cpuMode === "reserved"
              ? "Set aside on the node: tasks only go where these cores are free, and keep them when the node is busy."
              : "Shares the node's cores with the other shared tasks; it never stops a task from being placed."
          }
        >
          <div>
            <Segmented
              label="CPU mode"
              size="sm"
              value={f.cpuMode}
              onChange={(v) =>
                set(v === "shared" ? { cpuMode: v, cpu: DEFAULT_CPU } : { cpuMode: v, cpu: f.cpu === DEFAULT_CPU ? "0.5" : f.cpu })
              }
              options={modes}
            />
          </div>
        </ChoiceField>
        <div className="flex flex-wrap gap-2">
          {f.cpuMode === "reserved" && (
            <Field label="Cores reserved per task">
              <Input value={f.cpu} onChange={(e) => set({ cpu: num(e.target.value) })} inputMode="decimal" className="w-28" />
            </Field>
          )}
          <Field label="Limit (cores)" hint="Empty: no limit.">
            <Input
              value={f.cpuLimit}
              onChange={(e) => set({ cpuLimit: num(e.target.value) })}
              inputMode="decimal"
              placeholder="No limit"
              className="w-28"
            />
          </Field>
        </div>
        {f.cpuMode === "shared" && f.cpu !== DEFAULT_CPU && (
          <p className="text-faint text-xs">
            This service keeps a CPU weight of {f.cpu} from before (its share when the node is busy).{" "}
            <button type="button" className="text-accent hover:underline" onClick={() => set({ cpu: DEFAULT_CPU })}>
              Reset
            </button>
          </p>
        )}
      </div>
      <div className="border-line flex flex-col gap-2 rounded-md border p-2">
        <ChoiceField
          label="Memory"
          hint={
            f.memoryMode === "reserved"
              ? "Set aside on the node, used or not, and kept for the task when memory runs low."
              : "Uses what the node really has free; unused memory stays free for others."
          }
        >
          <div>
            <Segmented
              label="Memory mode"
              size="sm"
              value={f.memoryMode}
              onChange={(v) =>
                set(
                  v === "shared"
                    ? { memoryMode: v, memory: DEFAULT_MEMORY }
                    : { memoryMode: v, memory: f.memory === DEFAULT_MEMORY ? "512" : f.memory },
                )
              }
              options={modes}
            />
          </div>
        </ChoiceField>
        <div className="flex flex-wrap gap-2">
          {f.memoryMode === "reserved" && (
            <Field label="MiB reserved per task">
              <Input value={f.memory} onChange={(e) => set({ memory: num(e.target.value) })} inputMode="numeric" className="w-28" />
            </Field>
          )}
          <Field label="Limit (MiB)" hint="Empty: no limit. A task over it is restarted.">
            <Input
              value={f.memoryLimit}
              onChange={(e) => set({ memoryLimit: num(e.target.value) })}
              inputMode="numeric"
              placeholder="No limit"
              className="w-28"
            />
          </Field>
        </div>
        {f.memoryMode === "shared" && f.memory !== DEFAULT_MEMORY && (
          <p className="text-faint text-xs">
            Placement keeps {f.memory} MiB free for each task, from before.{" "}
            <button type="button" className="text-accent hover:underline" onClick={() => set({ memory: DEFAULT_MEMORY })}>
              Reset
            </button>
          </p>
        )}
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
      const { cpuMode: _c, memoryMode: _m, cpuLimit: _cl, memoryLimit: _ml, ...rest } = spec.resources;
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
