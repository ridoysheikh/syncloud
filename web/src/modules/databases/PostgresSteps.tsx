import { NodePicker } from "@/entities/nodes";
import { useS3Buckets, useS3Endpoints } from "@/lib/pg";
import { Field, Input } from "@/ui/controls";
import { ChoiceField } from "@/ui/choice";
import {
  PgAddonsField,
  PgParamsField,
  PgReplicationField,
  usePgCatalog,
} from "./pg/Config";
import {
  CountSelect,
  Section,
  SizeSelect,
  selectClass,
  type Form,
  type SetFn,
} from "./NewDatabaseWizard";

/** Capacity of a PostgreSQL cluster: member size, replicas, durability. */
export function PgCapacityStep({
  f,
  set,
}: {
  f: Pick<Form, "memMin" | "repMin" | "cpu" | "pg">;
  set: SetFn;
}) {
  return (
    <div className="flex flex-col gap-4 text-xs">
      <Section title="Each member">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <Field
            label="Memory"
            hint="Postgres is tuned from it: shared_buffers 25%, effective_cache_size 75%, max_connections."
          >
            <SizeSelect value={f.memMin} onChange={(v) => set("memMin", v)} />
          </Field>
          <Field label="CPU (cores)">
            <Input
              type="number"
              step="0.25"
              min={0.05}
              value={f.cpu}
              onChange={(e) => set("cpu", e.target.value)}
              className="max-w-40"
            />
          </Field>
        </div>
      </Section>
      <Section title="Replicas and failover">
        <ChoiceField label="Read replicas">
          <div>
            <CountSelect
              value={f.repMin}
              onChange={(v) => {
                set("repMin", v);
                const r = f.pg.replication;
                if (v === 0 && r.mode !== "async")
                  set("pg", { ...f.pg, replication: { ...r, mode: "async" } });
                else if (r.syncReplicas > Math.max(1, v))
                  set("pg", {
                    ...f.pg,
                    replication: { ...r, syncReplicas: Math.max(1, v) },
                  });
              }}
            />
          </div>
        </ChoiceField>
        <p className="text-faint">
          {f.repMin === 0
            ? "One server, no failover: if its node goes down the database is down until it returns."
            : "Replicas stream from the primary on other nodes and serve the read-only endpoint. Patroni promotes the most up-to-date replica within about 30 seconds if the primary fails, even while the controller is down."}
        </p>
        <PgReplicationField
          value={f.pg.replication}
          onChange={(v) => set("pg", { ...f.pg, replication: v })}
          replicas={f.repMin}
        />
      </Section>
    </div>
  );
}

/** Add-ons, parameters, backups and where members run. */
export function PgDataStep({
  f,
  set,
  backups,
  config,
}: {
  f: Pick<Form, "nodes" | "name"> &
    Partial<Pick<Form, "backupEndpoint" | "backupBucket" | "pg">>;
  set: SetFn;
  /** Offer WAL-G backups (the wizard; afterwards they live on the Backups tab). */
  backups?: boolean;
  /** Offer add-ons and parameters (the wizard; afterwards on the Configuration tab). */
  config?: boolean;
}) {
  const catalog = usePgCatalog();
  const pg = f.pg;
  const endpoints = useS3Endpoints();
  const buckets = useS3Buckets(f.backupEndpoint ?? "");
  return (
    <div className="flex flex-col gap-4 text-xs">
      <Section title="Database">
        <p className="text-muted">
          A database{" "}
          <code className="font-mono">
            {(f.name || "NAME").replace(/-/g, "_")}
          </code>{" "}
          owned by the user <code className="font-mono">app</code> is created.
          Data lives on node-local volumes with page checksums.
        </p>
      </Section>
      {config && pg && (
        <>
          <Section title="Add-ons">
            <PgAddonsField
              addons={catalog.addons}
              value={pg.extensions}
              onChange={(v) => {
                // Parameters of a disabled add-on go with it.
                const params = Object.fromEntries(
                  Object.entries(pg.parameters).filter(([k]) => {
                    const a = catalog.params.find((p) => p.name === k)?.addon;
                    return !a || v.includes(a);
                  }),
                );
                set("pg", { ...pg, extensions: v, parameters: params });
              }}
            />
          </Section>
          <Section title="Parameters (optional)">
            <p className="text-muted">
              The platform tunes memory settings from the member size; set only
              what you need. Everything here can be changed later on the
              Configuration tab.
            </p>
            <PgParamsField
              catalog={catalog.params}
              value={pg.parameters}
              onChange={(v) => set("pg", { ...pg, parameters: v })}
              addons={pg.extensions}
            />
          </Section>
        </>
      )}
      {backups && (
        <Section title="Backups and point-in-time recovery">
          <p className="text-muted">
            WAL-G archives every change to S3 within a minute and takes a daily
            base backup, encrypted; any moment of the last 7 days can be
            restored into a new database. Schedule and retention can be changed
            later on the Backups tab.
          </p>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Field label="S3 endpoint">
              <select
                className={selectClass}
                value={f.backupEndpoint ?? ""}
                onChange={(e) => {
                  set("backupEndpoint", e.target.value);
                  set("backupBucket", "");
                }}
              >
                <option value="">Off</option>
                {(endpoints.data ?? []).map((e) => (
                  <option key={e.id} value={e.name}>
                    {e.name} — {e.url}
                  </option>
                ))}
              </select>
            </Field>
            {f.backupEndpoint && (
              <Field label="Bucket">
                <select
                  className={selectClass}
                  value={f.backupBucket ?? ""}
                  onChange={(e) => set("backupBucket", e.target.value)}
                >
                  <option value="">choose…</option>
                  {(buckets.data ?? []).map((b) => (
                    <option key={b.name}>{b.name}</option>
                  ))}
                </select>
              </Field>
            )}
          </div>
          {endpoints.data?.length === 0 && (
            <p className="text-faint">
              No S3 endpoint is registered yet; add one under Storage.
            </p>
          )}
        </Section>
      )}
      <Section title="Nodes">
        <p className="text-muted">
          Optional: keep members on some nodes. Members always go on different
          nodes.
        </p>
        <NodePicker value={f.nodes} onChange={(v) => set("nodes", v)} />
      </Section>
    </div>
  );
}
