import { useNodes } from "@/lib/nodes";
import { useS3Buckets, useS3Endpoints } from "@/lib/pg";
import { Field, Input, Toggle } from "@/ui/controls";
import {
  CountSelect,
  Section,
  SizeSelect,
  selectClass,
  type Form,
  type SetFn,
} from "./NewDatabaseWizard";

/** Extensions in the PostgreSQL image, enabled per database. */
export const PG_EXTENSIONS: [string, string][] = [
  ["vector", "pgvector: embeddings and similarity search"],
  ["timescaledb", "TimescaleDB (Apache): hypertables and time functions"],
  ["pg_duckdb", "DuckDB analytics engine; reads Parquet on S3"],
  ["postgis", "PostGIS: geospatial types and indexes"],
  ["pg_partman", "Automatic partition creation and retention"],
  ["pg_cron", "Scheduled SQL jobs"],
  ["pg_stat_statements", "Query statistics"],
  ["hypopg", "Hypothetical indexes"],
  ["pg_trgm, pgcrypto, hstore, …", "The standard contrib modules"],
];

/** Capacity of a PostgreSQL cluster: member size, replicas, durability. */
export function PgCapacityStep({
  f,
  set,
}: {
  f: Pick<Form, "memMin" | "repMin" | "cpu" | "synchronous">;
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
        <Field label="Read replicas">
          <div className="max-w-40">
            <CountSelect value={f.repMin} onChange={(v) => set("repMin", v)} />
          </div>
        </Field>
        <p className="text-faint">
          {f.repMin === 0
            ? "One server, no failover: if its node goes down the database is down until it returns."
            : "Replicas stream from the primary on other nodes and serve the read-only endpoint. Patroni promotes the most up-to-date replica within about 30 seconds if the primary fails, even while the controller is down."}
        </p>
        <Toggle
          checked={f.synchronous}
          disabled={f.repMin === 0}
          onChange={(v) => set("synchronous", v)}
          label="Synchronous replication"
          hint="Each commit waits until a replica has it, so a failover never loses a committed transaction. Commits are slower by one network round trip."
        />
      </Section>
    </div>
  );
}

/** Extensions included, backups and where members run. */
export function PgDataStep({
  f,
  set,
  backups,
}: {
  f: Pick<Form, "nodes" | "name"> &
    Partial<Pick<Form, "backupEndpoint" | "backupBucket">>;
  set: SetFn;
  /** Offer WAL-G backups (the wizard; afterwards they live on the Backups tab). */
  backups?: boolean;
}) {
  const { data: nodes = [] } = useNodes();
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
      <Section title="Extensions included">
        <ul className="grid grid-cols-1 gap-x-4 gap-y-1 sm:grid-cols-2">
          {PG_EXTENSIONS.map(([name, help]) => (
            <li key={name}>
              <code className="font-mono">{name}</code>
              <span className="text-muted"> — {help}</span>
            </li>
          ))}
        </ul>
      </Section>
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
        <div className="flex flex-wrap gap-x-4 gap-y-1">
          {nodes.map((n) => (
            <label key={n.id} className="flex items-center gap-1.5">
              <input
                type="checkbox"
                checked={f.nodes.includes(n.name)}
                onChange={() =>
                  set(
                    "nodes",
                    f.nodes.includes(n.name)
                      ? f.nodes.filter((x) => x !== n.name)
                      : [...f.nodes, n.name],
                  )
                }
              />
              {n.name}
            </label>
          ))}
        </div>
      </Section>
    </div>
  );
}
