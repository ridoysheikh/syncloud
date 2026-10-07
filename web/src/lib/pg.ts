import { useQuery } from "@tanstack/react-query";
import { api } from "./api";

/** PostgreSQL explorer and administration (§13 "Explorer and administration"). */

export interface PgDatabase {
  name: string;
  owner: string;
  encoding: string;
  collation: string;
  sizeBytes: number;
  connections: number;
  connLimit: number;
  allowConnections: boolean;
  comment: string;
  /** The cluster's own database: the credentials point here. */
  primary: boolean;
  protected: boolean;
}

export interface PgMembership {
  role: string;
  admin: boolean;
  inherit: boolean;
}

export interface PgRole {
  name: string;
  login: boolean;
  createDb: boolean;
  createRole: boolean;
  inherit: boolean;
  superuser: boolean;
  replication: boolean;
  bypassRls: boolean;
  connLimit: number;
  validUntil: string | null;
  hasPassword: boolean;
  memberOf: PgMembership[] | null;
  members: string[] | null;
  owns: string[] | null;
  connections: number;
  comment: string;
  protected: boolean;
  predefined: boolean;
  app: boolean;
  /** Returned once, when a password was set or generated. */
  password?: string;
}

export type PgObjectKind =
  | "table"
  | "partitioned"
  | "view"
  | "matview"
  | "foreign"
  | "sequence"
  | "function"
  | "procedure"
  | "aggregate"
  | "type";

export interface PgObject {
  name: string;
  kind: PgObjectKind;
  oid: number;
  owner: string;
  rowEstimate?: number;
  sizeBytes?: number;
  partitions?: number;
  comment?: string;
}

export interface PgSchema {
  name: string;
  owner: string;
  system: boolean;
  comment?: string;
  objects: PgObject[];
}

export interface PgColumn {
  name: string;
  type: string;
  notNull: boolean;
  default?: string;
  identity?: string;
  generated?: string;
  comment?: string;
}

export interface PgObjectDetail extends PgObject {
  schema: string;
  tableBytes?: number;
  indexBytes?: number;
  toastBytes?: number;
  liveRows?: number;
  deadRows?: number;
  seqScans?: number;
  indexScans?: number;
  lastVacuum?: string;
  lastAutovacuum?: string;
  lastAnalyze?: string;
  columns: PgColumn[];
  constraints: {
    name: string;
    kind: string;
    definition: string;
    references?: string;
  }[];
  indexes: {
    name: string;
    definition: string;
    primary: boolean;
    unique: boolean;
    valid: boolean;
    sizeBytes: number;
    scans: number;
  }[];
  triggers: { name: string; definition: string; enabled: boolean }[];
  partitionOf?: string;
  partitionKey?: string;
  partitionList?: string[];
  ddl: string;
}

export interface PgRowFilter {
  column: string;
  op: string;
  value: string;
}

export interface PgRows {
  columns: { name: string; type: string }[];
  rows: (string | null)[][];
  more: boolean;
  total?: number;
}

export type PgPrivKind =
  "database" | "schema" | "table" | "sequence" | "function";

export interface PgObjectRef {
  kind: PgPrivKind;
  schema?: string;
  name?: string;
  oid?: number;
}

export interface PgGrant {
  grantee: string;
  /** privilege -> with grant option */
  privileges: Record<string, boolean>;
  grantors?: Record<string, string>;
}

export interface PgPrivileges {
  kind: PgPrivKind;
  object: string;
  owner: string;
  available: string[];
  grants: PgGrant[];
  defaults?: {
    forRole: string;
    objectType: string;
    grantee: string;
    privileges: Record<string, boolean>;
  }[];
  role?: string;
  effective?: Record<string, boolean>;
}

export interface PgPrivilegeChange {
  revoke?: boolean;
  privileges: string[];
  grantee: string;
  grantOption?: boolean;
  target?: string;
  object: PgObjectRef | { schema: string };
  forRole?: string;
}

export interface PgExtension {
  name: string;
  defaultVersion: string;
  installedVersion?: string;
  schema?: string;
  comment: string;
  preloaded: boolean;
}

export interface PgStatementResult {
  statement: string;
  columns: { name: string; type: string }[];
  rows: (string | null)[][];
  truncated: boolean;
  tag: string;
  rowsAffected: number;
  durationMs: number;
}

export interface PgQueryResult {
  results: PgStatementResult[];
  error?: {
    statement: number;
    message: string;
    code?: string;
    detail?: string;
    hint?: string;
    position?: number;
  };
  rolledBack?: boolean;
  readOnly: boolean;
  role: string;
  database: string;
}

export interface PgSession {
  pid: number;
  user: string;
  database: string;
  client: string;
  application: string;
  state: string;
  waitEvent: string;
  query: string;
  backendStart: string | null;
  xactStart: string | null;
  queryStart: string | null;
  blockedBy: number[] | null;
  platform: boolean;
}

const withDb = (url: string, db: string) =>
  db
    ? `${url}${url.includes("?") ? "&" : "?"}db=${encodeURIComponent(db)}`
    : url;

export function usePgDatabases(path: string) {
  return useQuery({
    queryKey: ["pg", path, "databases"],
    queryFn: () =>
      api<{ items: PgDatabase[] }>("GET", `${path}/pg/databases`).then(
        (r) => r.items,
      ),
  });
}

export function usePgRoles(path: string) {
  return useQuery({
    queryKey: ["pg", path, "roles"],
    queryFn: () =>
      api<{ items: PgRole[] }>("GET", `${path}/pg/roles`).then((r) => r.items),
  });
}

export function usePgSchema(path: string, db: string, system: boolean) {
  return useQuery({
    queryKey: ["pg", path, "schema", db, system],
    queryFn: () =>
      api<{ schemas: PgSchema[] }>(
        "GET",
        withDb(`${path}/pg/schema?system=${system}`, db),
      ).then((r) => r.schemas),
  });
}

export function usePgObject(
  path: string,
  db: string,
  schema: string,
  name: string,
  oid?: number,
) {
  const q = oid
    ? `oid=${oid}`
    : `schema=${encodeURIComponent(schema)}&name=${encodeURIComponent(name)}`;
  return useQuery({
    queryKey: ["pg", path, "object", db, schema, name, oid],
    queryFn: () =>
      api<PgObjectDetail>("GET", withDb(`${path}/pg/object?${q}`, db)),
  });
}

export function usePgPrivileges(
  path: string,
  db: string,
  ref: PgObjectRef,
  role: string,
) {
  const q = new URLSearchParams({ kind: ref.kind });
  if (ref.schema) q.set("schema", ref.schema);
  if (ref.name) q.set("name", ref.name);
  if (ref.oid) q.set("oid", String(ref.oid));
  if (role) q.set("role", role);
  return useQuery({
    queryKey: ["pg", path, "privileges", db, ref, role],
    queryFn: () =>
      api<PgPrivileges>("GET", withDb(`${path}/pg/privileges?${q}`, db)),
  });
}

export function usePgExtensions(path: string, db: string) {
  return useQuery({
    queryKey: ["pg", path, "extensions", db],
    queryFn: () =>
      api<{ items: PgExtension[] }>(
        "GET",
        withDb(`${path}/pg/extensions`, db),
      ).then((r) => r.items),
  });
}

export function usePgSessions(path: string, all: boolean) {
  return useQuery({
    queryKey: ["pg", path, "sessions", all],
    queryFn: () =>
      api<{ items: PgSession[] }>("GET", `${path}/pg/sessions?all=${all}`).then(
        (r) => r.items,
      ),
    refetchInterval: 5000,
  });
}

export const pgUrl = withDb;
