import { useEffect, useMemo, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Lock, ShieldCheck, Zap } from "lucide-react";
import { api } from "@/lib/api";
import { bytes } from "@/lib/nodes";
import {
  defaultPgReplication,
  useDatabaseEngines,
  type Database,
  type PgAddon,
  type PgParam,
  type PgReplicationSpec,
} from "@/lib/databases";
import {
  usePgReplication,
  usePgSettings,
  type PgClusterMember,
  type PgReplica,
  type PgSetting,
  type PgSlot,
} from "@/lib/pg";
import { Panel } from "@/ui/Panel";
import { StatTile } from "@/ui/StatTile";
import { DataTable, type Column } from "@/ui/DataTable";
import {
  Alert,
  Button,
  Field,
  Input,
  StatusBadge,
  Toggle,
} from "@/ui/controls";
import { cn, gap } from "@/ui/cn";
import { ChoiceCards, ChoiceField, Segmented } from "@/ui/choice";
import { CountSelect, SizeSelect } from "../NewDatabaseWizard";
import { errText } from "./shared";
import { Select } from "@/ui/select";

/** The PostgreSQL engine's add-ons and parameter catalog. */
export function usePgCatalog() {
  const { data: engines = [] } = useDatabaseEngines();
  const pg = engines.find((e) => e.name === "postgres");
  return { addons: pg?.addons ?? [], params: pg?.parameters ?? [] };
}

// ── add-ons ──

/** Toggles for the optional extensions. */
export function PgAddonsField({
  addons,
  value,
  onChange,
}: {
  addons: PgAddon[];
  value: string[];
  onChange: (v: string[]) => void;
}) {
  return (
    <div className="flex flex-col gap-2">
      <p className="text-muted">
        A new database is plain PostgreSQL with WAL-G backups and query
        statistics (pg_stat_statements). Add-ons are opt-in: enable one here,
        then install it with CREATE EXTENSION (or under Explorer) in the
        databases that use it. Contrib modules such as pg_trgm, pgcrypto and
        hstore are always available.
      </p>
      <div className="grid grid-cols-1 gap-x-4 gap-y-2 sm:grid-cols-2">
        {addons.map((a) => (
          <Toggle
            key={a.name}
            checked={value.includes(a.name)}
            onChange={(on) =>
              onChange(
                on
                  ? [...value, a.name].sort()
                  : value.filter((x) => x !== a.name),
              )
            }
            label={
              <>
                {a.title}{" "}
                {a.preload && (
                  <span className="text-faint">(preloaded library)</span>
                )}
              </>
            }
            hint={a.description}
          />
        ))}
      </div>
    </div>
  );
}

// ── replication ──

/** Replication mode and failover settings. */
export function PgReplicationField({
  value,
  onChange,
  replicas,
  advanced = true,
}: {
  value: PgReplicationSpec;
  onChange: (v: PgReplicationSpec) => void;
  replicas: number;
  advanced?: boolean;
}) {
  const set = <K extends keyof PgReplicationSpec>(
    k: K,
    v: PgReplicationSpec[K],
  ) => onChange({ ...value, [k]: v });
  const num = (k: keyof PgReplicationSpec, label: string, hint: string) => (
    <Field label={label} hint={hint}>
      <Input
        type="number"
        min={0}
        value={value[k] as number}
        onChange={(e) => set(k, Number(e.target.value) as never)}
        className="max-w-40"
      />
    </Field>
  );
  return (
    <div className="flex flex-col gap-3">
      <ChoiceCards
        label="Replication mode"
        value={value.mode}
        onChange={(v) => set("mode", v)}
        columns={3}
        options={[
          {
            value: "async",
            title: "Asynchronous",
            description:
              "Commits return at once; a failover can lose the last moments of writes.",
            icon: Zap,
          },
          {
            value: "sync",
            title: "Synchronous",
            description:
              "Each commit waits until replicas have it: a failover loses nothing. With no replica left, writes continue unprotected.",
            icon: ShieldCheck,
            disabled: replicas === 0,
          },
          {
            value: "strict",
            title: "Synchronous, strict",
            description:
              "Like synchronous, but writes stop rather than continue without a replica that has them.",
            icon: Lock,
            disabled: replicas === 0,
          },
        ]}
      />
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        {value.mode !== "async" && (
          <ChoiceField
            label="Replicas that confirm each commit"
            hint="More is safer, and slower."
          >
            <Segmented
              label="Replicas that confirm each commit"
              value={String(value.syncReplicas)}
              onChange={(v) => set("syncReplicas", Number(v))}
              options={Array.from(
                { length: Math.max(1, replicas) },
                (_, i) => ({
                  value: String(i + 1),
                  label: String(i + 1),
                }),
              )}
            />
          </ChoiceField>
        )}
      </div>
      {replicas === 0 && (
        <p className="text-faint">Synchronous modes need a replica.</p>
      )}
      {advanced && (
        <details className="flex flex-col gap-3">
          <summary className="text-muted cursor-pointer select-none">
            Failover and WAL retention
          </summary>
          <div className="mt-2 grid grid-cols-1 gap-3 sm:grid-cols-2">
            {num(
              "failoverTtl",
              "Leader lease (s)",
              "A leader silent this long is replaced; 15–300.",
            )}
            {num(
              "maxLagOnFailover",
              "Max lag to be promoted (MiB)",
              "A replica further behind is never promoted.",
            )}
            {num(
              "walKeepSize",
              "WAL kept for replicas (MiB)",
              "For replicas without a slot.",
            )}
            {num(
              "maxSlotWalKeepSize",
              "WAL a slot may hold (MiB)",
              "0 = unlimited: a stopped replica can then fill the disk.",
            )}
            <Toggle
              checked={value.slots}
              onChange={(v) => set("slots", v)}
              label="Replication slots"
              hint="The primary keeps the WAL each replica still needs."
            />
            <Toggle
              checked={value.hotStandbyFeedback}
              onChange={(v) => set("hotStandbyFeedback", v)}
              label="Hot standby feedback"
              hint="Long queries on replicas are not cancelled by vacuum; the primary may bloat meanwhile."
            />
          </div>
        </details>
      )}
    </div>
  );
}

// ── parameters ──

/** Grouped editor for the curated parameters; "" means the default. */
export function PgParamsField({
  catalog,
  value,
  onChange,
  addons,
  live,
}: {
  catalog: PgParam[];
  value: Record<string, string>;
  onChange: (v: Record<string, string>) => void;
  addons: string[] | null;
  live?: PgSetting[];
}) {
  const [filter, setFilter] = useState("");
  const liveOf = useMemo(
    () => new Map((live ?? []).map((s) => [s.name, s])),
    [live],
  );
  const groups = useMemo(() => {
    const g = new Map<string, PgParam[]>();
    for (const p of catalog) {
      if (p.addon && addons && !addons.includes(p.addon)) continue;
      if (
        filter &&
        !p.name.includes(filter.toLowerCase()) &&
        !p.description.toLowerCase().includes(filter.toLowerCase())
      )
        continue;
      g.set(p.group, [...(g.get(p.group) ?? []), p]);
    }
    return [...g];
  }, [catalog, addons, filter]);
  const put = (k: string, v: string) => {
    const next = { ...value };
    if (v === "") delete next[k];
    else next[k] = v;
    onChange(next);
  };
  return (
    <div className="flex flex-col gap-2">
      <Input
        placeholder="Filter parameters…"
        value={filter}
        onChange={(e) => setFilter(e.target.value)}
        className="max-w-72"
      />
      {groups.map(([group, ps]) => (
        <details
          key={group}
          open={!!filter || ps.some((p) => value[p.name] !== undefined)}
          className="border-line rounded-sm border"
        >
          <summary className="text-muted cursor-pointer px-2 py-1.5 select-none">
            {group}{" "}
            <span className="text-faint">
              ({ps.filter((p) => value[p.name] !== undefined).length} set)
            </span>
          </summary>
          <div className="divide-line flex flex-col divide-y">
            {ps.map((p) => {
              const l = liveOf.get(p.name);
              return (
                <div
                  key={p.name}
                  className="grid grid-cols-1 items-center gap-1 px-2 py-1.5 sm:grid-cols-[1fr_12rem]"
                >
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-1.5">
                      <code className="font-mono">{p.name}</code>
                      {p.restart && <span className="text-faint">restart</span>}
                      {l?.pendingRestart && (
                        <StatusBadge tone="warn">restart pending</StatusBadge>
                      )}
                    </div>
                    <div className="text-faint">
                      {p.description}
                      {l && (
                        <>
                          {" "}
                          Now <span className="font-mono">{l.value}</span>
                          {l.platformDefault && !l.configured
                            ? " (platform default)"
                            : ""}
                          .
                        </>
                      )}
                    </div>
                  </div>
                  <ParamInput
                    p={p}
                    value={value[p.name] ?? ""}
                    placeholder={l?.value ?? "default"}
                    onChange={(v) => put(p.name, v)}
                  />
                </div>
              );
            })}
          </div>
        </details>
      ))}
    </div>
  );
}

function ParamInput({
  p,
  value,
  placeholder,
  onChange,
}: {
  p: PgParam;
  value: string;
  placeholder: string;
  onChange: (v: string) => void;
}) {
  const options =
    p.type === "bool" ? ["on", "off"] : p.type === "enum" ? p.enum : null;
  if (options)
    return (
      <Select
        aria-label={p.name}
        value={value}
        onChange={(v) => onChange(v)}
        size="sm"
        className="w-full"
      >
        <option value="">default ({placeholder})</option>
        {options.map((o) => (
          <option key={o}>{o}</option>
        ))}
      </Select>
    );
  const hint =
    p.type === "memory"
      ? "e.g. 64MB"
      : p.type === "time"
        ? "e.g. 30s"
        : `${p.min ?? ""}–${p.max ?? ""}`;
  return (
    <Input
      aria-label={p.name}
      value={value}
      placeholder={`${placeholder} (${hint})`}
      onChange={(e) => onChange(e.target.value.trim())}
      className="font-mono"
    />
  );
}

// ── the Configuration tab ──

/** Add-ons, replication and parameters of a running cluster. */
export function PgConfigTab({ d, path }: { d: Database; path: string }) {
  const qc = useQueryClient();
  const { addons, params } = usePgCatalog();
  const live = usePgSettings(path);
  const pg = d.spec.postgres;
  const init = useMemo(
    () => ({
      extensions: pg?.extensions ?? null,
      parameters: pg?.parameters ?? {},
      replication: { ...defaultPgReplication, ...pg?.replication },
      maxConnections: pg?.maxConnections ?? 0,
      replicas: d.spec.replicas.min,
      memory: d.spec.memory.min,
      cpu: d.spec.cpu,
    }),
    [pg, d.spec],
  );
  const [f, setF] = useState(init);
  useEffect(() => setF(init), [init]);
  const dirty = JSON.stringify(f) !== JSON.stringify(init);
  const save = useMutation({
    mutationFn: () =>
      api("PUT", path, {
        spec: {
          ...d.spec,
          // PostgreSQL clusters have a fixed size until autoscaling (13d).
          replicas: { min: f.replicas, max: f.replicas },
          memory: { min: f.memory, max: f.memory },
          cpu: f.cpu,
          postgres: {
            ...pg,
            extensions: f.extensions ?? undefined,
            parameters: f.parameters,
            replication: f.replication,
            maxConnections: f.maxConnections,
            synchronous: f.replication.mode !== "async",
          },
        },
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["databases"] });
      void qc.invalidateQueries({ queryKey: ["pg", path] });
    },
  });
  const pending = (live.data?.items ?? []).filter((s) => s.pendingRestart);
  const legacy = f.extensions === null;
  return (
    <div className={cn("flex flex-col", gap)}>
      {pending.length > 0 && (
        <Alert tone="info">
          {pending.map((s) => s.name).join(", ")}{" "}
          {pending.length === 1 ? "waits" : "wait"} for a restart: the members
          restart one at a time, replicas first, then the primary.
        </Alert>
      )}
      <div className={cn("grid grid-cols-1 items-start xl:grid-cols-2", gap)}>
        <Panel title="Add-ons">
          <div className="flex flex-col gap-2 text-xs">
            {legacy ? (
              <>
                <p className="text-muted">
                  This database predates optional add-ons, so all of them are
                  enabled.
                </p>
                <div>
                  <Button
                    onClick={() =>
                      setF({ ...f, extensions: addons.map((a) => a.name) })
                    }
                  >
                    Choose add-ons
                  </Button>
                </div>
              </>
            ) : (
              <PgAddonsField
                addons={addons}
                value={f.extensions ?? []}
                onChange={(v) => setF({ ...f, extensions: v })}
              />
            )}
            <p className="text-faint">
              Enabling one with a library restarts the members one at a time.
              Disabling one is refused while a database still has it installed.
            </p>
          </div>
        </Panel>
        <Panel title="Size and replication">
          <div className="flex flex-col gap-3 text-xs">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <ChoiceField
                label="Read replicas"
                hint="Added replicas clone from the newest base backup when backups are on."
              >
                <CountSelect
                  value={f.replicas}
                  onChange={(v) =>
                    setF({
                      ...f,
                      replicas: v,
                      replication:
                        v === 0
                          ? { ...f.replication, mode: "async" }
                          : {
                              ...f.replication,
                              syncReplicas: Math.min(
                                f.replication.syncReplicas,
                                v,
                              ),
                            },
                    })
                  }
                />
              </ChoiceField>
              <Field
                label="Memory per member"
                hint="Restarts the members one at a time."
              >
                <SizeSelect
                  value={f.memory}
                  onChange={(v) => setF({ ...f, memory: v })}
                />
              </Field>
              <Field label="CPU (cores)">
                <Input
                  type="number"
                  step="0.25"
                  min={0.05}
                  value={f.cpu}
                  onChange={(e) => setF({ ...f, cpu: Number(e.target.value) })}
                />
              </Field>
            </div>
            <PgReplicationField
              value={f.replication}
              onChange={(v) => setF({ ...f, replication: v })}
              replicas={f.replicas}
            />
            <Field
              label="Max connections"
              hint="0 derives it from memory (about one per 8 MiB). Changing it restarts the members one at a time."
            >
              <Input
                type="number"
                min={0}
                max={5000}
                value={f.maxConnections}
                onChange={(e) =>
                  setF({ ...f, maxConnections: Number(e.target.value) })
                }
                className="max-w-40"
              />
            </Field>
            <p className="text-faint">
              Replication settings apply within seconds, without restarts. New
              replicas join on other nodes.
            </p>
          </div>
        </Panel>
      </div>
      <Panel title="Parameters">
        <div className="flex flex-col gap-2 text-xs">
          <p className="text-muted">
            Leave a field empty for the platform's value (memory settings follow
            the member size). Settings marked restart are applied by restarting
            members one at a time; the rest reload at once.
          </p>
          {live.error && <Alert tone="warn">{errText(live.error)}</Alert>}
          <PgParamsField
            catalog={params}
            value={f.parameters}
            onChange={(v) => setF({ ...f, parameters: v })}
            addons={f.extensions}
            live={live.data?.items}
          />
        </div>
      </Panel>
      {save.error && <Alert>{errText(save.error)}</Alert>}
      <div className="flex items-center gap-2">
        <Button
          variant="primary"
          disabled={!dirty || save.isPending}
          onClick={() => save.mutate()}
        >
          Save configuration
        </Button>
        <Button
          variant="ghost"
          disabled={!dirty || save.isPending}
          onClick={() => setF(init)}
        >
          Discard
        </Button>
      </div>
    </div>
  );
}

// ── the Replication tab ──

const ms = (v: number) =>
  v >= 1000 ? `${(v / 1000).toFixed(1)} s` : `${v.toFixed(0)} ms`;

/** Live replication: replicas as the primary sees them, members, slots. */
export function PgReplicationTab({ path }: { path: string }) {
  const r = usePgReplication(path);
  const s = r.data;
  const replicas: Column<PgReplica>[] = [
    {
      header: "Replica",
      cell: (x) => <span className="font-mono">{x.name}</span>,
    },
    { header: "Address", cell: (x) => x.clientAddr || "—" },
    {
      header: "State",
      cell: (x) => (
        <StatusBadge tone={x.state === "streaming" ? "ok" : "warn"}>
          {x.state}
        </StatusBadge>
      ),
    },
    { header: "Sync", cell: (x) => x.syncState },
    { header: "Behind", cell: (x) => bytes(x.lagBytes) },
    { header: "Write lag", cell: (x) => ms(x.writeLagMs) },
    { header: "Flush lag", cell: (x) => ms(x.flushLagMs) },
    { header: "Replay lag", cell: (x) => ms(x.replayLagMs) },
    {
      header: "Replayed to",
      cell: (x) => <span className="font-mono">{x.replayLsn}</span>,
    },
  ];
  const members: Column<PgClusterMember>[] = [
    {
      header: "Member",
      cell: (x) => <span className="font-mono">{x.name}</span>,
    },
    { header: "Role", cell: (x) => x.role },
    {
      header: "State",
      cell: (x) => (
        <StatusBadge
          tone={
            x.state === "running" || x.state === "streaming" ? "ok" : "warn"
          }
        >
          {x.state}
        </StatusBadge>
      ),
    },
    { header: "Timeline", cell: (x) => x.timeline },
    {
      header: "Lag",
      cell: (x) => (typeof x.lag === "number" ? bytes(x.lag) : (x.lag ?? "—")),
    },
    {
      header: "Restart",
      cell: (x) => (x.pendingRestart ? "pending" : "—"),
    },
  ];
  const slots: Column<PgSlot>[] = [
    {
      header: "Slot",
      cell: (x) => <span className="font-mono">{x.name}</span>,
    },
    { header: "Type", cell: (x) => x.type },
    { header: "Active", cell: (x) => (x.active ? "yes" : "no") },
    { header: "WAL", cell: (x) => x.walStatus || "—" },
    { header: "Holds", cell: (x) => bytes(x.heldBytes) },
  ];
  const maxLag = Math.max(0, ...(s?.replicas ?? []).map((x) => x.lagBytes));
  const mode = s?.settings.mode ?? "async";
  return (
    <div className={cn("flex flex-col", gap)}>
      {r.error && <Alert>{errText(r.error)}</Alert>}
      <div className={cn("grid grid-cols-2 md:grid-cols-4", gap)}>
        <StatTile
          label="Mode"
          value={
            mode === "async" ? "async" : mode === "sync" ? "sync" : "strict"
          }
          hint={
            mode === "async"
              ? "commits do not wait"
              : `${s?.settings.syncReplicas ?? 1} replica(s) confirm each commit`
          }
        />
        <StatTile
          label="Streaming"
          value={`${(s?.replicas ?? []).filter((x) => x.state === "streaming").length}`}
          hint="replicas connected to the primary"
        />
        <StatTile
          label="Max behind"
          value={bytes(maxLag)}
          tone={maxLag > 64 << 20 ? "warn" : undefined}
          hint={`promotable within ${s?.settings.maxLagOnFailover ?? 1} MiB`}
        />
        <StatTile
          label="Timeline"
          value={s?.timeline ?? "—"}
          hint={s ? `at ${s.currentLsn}` : undefined}
        />
      </div>
      <Panel title="Replicas (from the primary)" flush>
        <DataTable
          columns={replicas}
          rows={s?.replicas ?? []}
          rowKey={(x) => x.name + x.clientAddr}
          empty={
            <p className="text-faint px-2 py-3 text-xs md:px-3">
              No replica is connected.
            </p>
          }
        />
      </Panel>
      <div className={cn("grid grid-cols-1 items-start xl:grid-cols-2", gap)}>
        <Panel title="Members (Patroni)" flush>
          <DataTable
            columns={members}
            rows={s?.members ?? []}
            rowKey={(x) => x.name}
          />
        </Panel>
        <Panel title="Replication slots" flush>
          <DataTable
            columns={slots}
            rows={s?.slots ?? []}
            rowKey={(x) => x.name}
            empty={
              <p className="text-faint px-2 py-3 text-xs md:px-3">No slots.</p>
            }
          />
        </Panel>
      </div>
    </div>
  );
}
