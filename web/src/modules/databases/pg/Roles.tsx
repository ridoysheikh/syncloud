import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Copy, Plus, RefreshCw, Search, ShieldCheck } from "lucide-react";
import { api } from "@/lib/api";
import { dbPath, useDatabases } from "@/lib/databases";
import {
  usePgDatabases,
  usePgRoles,
  type PgMembership,
  type PgRole,
} from "@/lib/pg";
import { PageHeader } from "@/ui/PageHeader";
import { Panel } from "@/ui/Panel";
import { DataTable } from "@/ui/DataTable";
import { EmptyState } from "@/ui/EmptyState";
import { Alert, Button, Field, IconButton, Input, Toggle } from "@/ui/controls";
import { ChoiceCard } from "@/ui/choice";
import { cn, gap } from "@/ui/cn";
import { selectClass } from "../NewDatabaseWizard";
import { errText, inlineSelect } from "./shared";

/** Predefined roles worth granting, with what they give. */
const PREDEFINED: [string, string][] = [
  ["pg_read_all_data", "read every table, view and sequence"],
  ["pg_write_all_data", "write every table and sequence"],
  ["pg_monitor", "read monitoring views and statistics"],
  ["pg_read_all_stats", "read all pg_stat_* views"],
  ["pg_read_all_settings", "read every setting"],
  ["pg_signal_backend", "cancel or end other sessions"],
  ["pg_maintain", "VACUUM, ANALYZE, REINDEX, REFRESH on everything"],
  ["pg_checkpoint", "run CHECKPOINT"],
  ["pg_create_subscription", "create logical replication subscriptions"],
  ["pg_use_reserved_connections", "use the reserved connection slots"],
];

const rolesTo = (name: string) =>
  `/databases/${encodeURIComponent(name)}?tab=roles`;

/** The cluster's roles. */
export function PgRoles({ path, name }: { path: string; name: string }) {
  const roles = usePgRoles(path);
  const [predefined, setPredefined] = useState(false);
  const navigate = useNavigate();
  const rows = (roles.data ?? []).filter((r) => predefined || !r.predefined);
  const open = (r: PgRole) =>
    void navigate({
      to: `/databases/${encodeURIComponent(name)}/roles/${encodeURIComponent(r.name)}` as string,
    });
  return (
    <Panel
      title="Roles"
      flush
      actions={
        <>
          <Toggle
            checked={predefined}
            onChange={setPredefined}
            label="Predefined pg_* roles"
          />
          <IconButton label="Reload" onClick={() => void roles.refetch()}>
            <RefreshCw
              className={cn("size-3.5", roles.isFetching && "animate-spin")}
            />
          </IconButton>
          <Link
            to={`/databases/${encodeURIComponent(name)}/roles/new` as string}
          >
            <Button variant="primary">
              <Plus className="size-3.5" /> New role
            </Button>
          </Link>
        </>
      }
    >
      {roles.error && (
        <div className="p-2">
          <Alert>{errText(roles.error)}</Alert>
        </div>
      )}
      <DataTable
        rows={rows}
        rowKey={(r) => r.name}
        empty={<EmptyState icon={Search} title="No roles" />}
        columns={[
          {
            header: "Role",
            cell: (r) => (
              <button
                type="button"
                className="hover:text-accent font-mono"
                onClick={() => open(r)}
              >
                {r.name}
                {r.app && (
                  <span className="text-faint ml-1 font-sans">credentials</span>
                )}
                {r.protected && !r.predefined && (
                  <span className="text-faint ml-1 font-sans">platform</span>
                )}
              </button>
            ),
          },
          {
            header: "Login",
            cell: (r) =>
              r.login ? "yes" : <span className="text-faint">no</span>,
          },
          { header: "Attributes", cell: (r) => attributes(r) },
          {
            header: "Member of",
            className: "whitespace-normal",
            cell: (r) =>
              (r.memberOf ?? [])
                .map((m) => m.role + (m.admin ? " (admin)" : ""))
                .join(", "),
          },
          { header: "Owns", cell: (r) => (r.owns ?? []).join(", ") },
          {
            header: "Expires",
            cell: (r) =>
              r.validUntil ? new Date(r.validUntil).toLocaleDateString() : "",
          },
          { header: "Sessions", cell: (r) => r.connections || "" },
        ]}
      />
    </Panel>
  );
}

function attributes(r: PgRole) {
  const a: string[] = [];
  if (r.superuser) a.push("superuser");
  if (r.replication) a.push("replication");
  if (r.createDb) a.push("create databases");
  if (r.createRole) a.push("create roles");
  if (!r.inherit) a.push("no inherit");
  if (r.connLimit >= 0) a.push(`limit ${r.connLimit}`);
  return a.join(", ");
}

type PasswordMode = "keep" | "generate" | "set" | "none";

interface RoleForm {
  name: string;
  comment: string;
  login: boolean;
  password: PasswordMode;
  passwordText: string;
  validUntil: string; // yyyy-mm-dd, "" = never
  connLimit: string;
  createDb: boolean;
  createRole: boolean;
  inherit: boolean;
  memberOf: PgMembership[];
  access: "" | "read" | "write"; // new roles: on the cluster database
}

const fromRole = (r?: PgRole): RoleForm => ({
  name: r?.name ?? "",
  comment: r?.comment ?? "",
  login: r?.login ?? true,
  password: r ? "keep" : "generate",
  passwordText: "",
  validUntil: r?.validUntil ? r.validUntil.slice(0, 10) : "",
  connLimit: r && r.connLimit >= 0 ? String(r.connLimit) : "",
  createDb: r?.createDb ?? false,
  createRole: r?.createRole ?? false,
  inherit: r?.inherit ?? true,
  memberOf: r?.memberOf ?? [],
  access: "",
});

/** Create or change a role: a full page (§10: wizards and forms are pages). */
export function PgRolePage() {
  const { name, role } = useParams({ strict: false }) as {
    name: string;
    role?: string;
  };
  const creating = !role;
  const path = dbPath(name);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { data: all } = useDatabases();
  const cluster = all?.find((d) => d.name === name);
  const roles = usePgRoles(path);
  const dbs = usePgDatabases(path);
  const current = roles.data?.find((r) => r.name === role);
  const [f, setF] = useState<RoleForm>(fromRole());
  const [shown, setShown] = useState<{ role: string; password: string } | null>(
    null,
  );
  useEffect(() => {
    if (current) setF(fromRole(current));
  }, [current]);
  const set = <K extends keyof RoleForm>(k: K, v: RoleForm[K]) =>
    setF((x) => ({ ...x, [k]: v }));
  const locked = !!current?.protected;
  const managed = !!current?.app;

  const body = () => {
    const b: Record<string, unknown> = {
      createDb: f.createDb,
      createRole: f.createRole,
      inherit: f.inherit,
      memberOf: f.memberOf,
      comment: f.comment,
      connLimit: f.connLimit.trim() === "" ? -1 : Number(f.connLimit),
    };
    if (!managed) {
      b.login = f.login;
      b.validUntil = f.validUntil ? `${f.validUntil}T00:00:00Z` : "";
      if (f.password === "generate") b.generatePassword = true;
      if (f.password === "set") b.password = f.passwordText;
      if (f.password === "none") b.password = "";
    }
    return b;
  };
  const save = useMutation({
    mutationFn: async () => {
      if (creating) {
        const r = await api<PgRole>("POST", `${path}/pg/roles`, {
          ...body(),
          name: f.name,
        });
        if (f.access) {
          await api("POST", `${path}/pg/privileges`, {
            preset: { role: f.name, access: f.access },
          });
        }
        return r;
      }
      const b = body();
      if (f.name !== role) b.name = f.name;
      return api<PgRole>(
        "PUT",
        `${path}/pg/roles/${encodeURIComponent(role ?? "")}`,
        b,
      );
    },
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: ["pg", path] });
      if (!r.password) {
        void navigate({ to: rolesTo(name) });
        return;
      }
      // Stay to show the password once (navigating would remount the page
      // and lose it); Done goes back to the list.
      setShown({ role: r.name, password: r.password });
      setF((x) => ({ ...x, password: "keep", passwordText: "" }));
    },
  });

  const members = useMemo(() => {
    const own = (roles.data ?? [])
      .filter(
        (r) => !r.protected && !r.predefined && r.name !== (role ?? f.name),
      )
      .map((r): [string, string] => [
        r.name,
        r.app
          ? "owner of the cluster's databases: full rights on everything it owns"
          : r.login
            ? "login role"
            : "group role",
      ]);
    return [...own, ...PREDEFINED];
  }, [roles.data, role, f.name]);
  const isMember = (r: string) => f.memberOf.find((m) => m.role === r);
  const toggleMember = (r: string) =>
    set(
      "memberOf",
      isMember(r)
        ? f.memberOf.filter((m) => m.role !== r)
        : [...f.memberOf, { role: r, admin: false, inherit: true }],
    );

  const url = (pw: string, who: string) =>
    cluster
      ? `postgresql://${who}:${pw}@${cluster.host}:${cluster.port}/${name.replace(/-/g, "_")}`
      : "";

  return (
    <div className={cn("mx-auto flex w-full max-w-4xl flex-col", gap)}>
      <PageHeader
        crumbs={[
          <Link to={"/databases" as string} className="hover:text-fg">
            Databases
          </Link>,
          <Link to={rolesTo(name)} className="hover:text-fg">
            {name}
          </Link>,
          <Link to={rolesTo(name)} className="hover:text-fg">
            roles
          </Link>,
        ]}
        title={creating ? "New role" : (role ?? "")}
      />
      {roles.error && <Alert>{errText(roles.error)}</Alert>}
      {locked && (
        <Alert tone="info">
          {current?.predefined
            ? "A predefined PostgreSQL role. Grant it to your roles under Membership."
            : "A platform role: SynCloud uses it for replication and administration, so it cannot be changed."}
        </Alert>
      )}
      {shown && (
        <Panel title="Password">
          <div className="flex flex-col gap-2 text-xs">
            <Alert tone="warn">
              Copy it now: it is shown once and SynCloud does not keep it.
            </Alert>
            <Secret label="Password" value={shown.password} />
            {url(shown.password, shown.role) && (
              <Secret label="URL" value={url(shown.password, shown.role)} />
            )}
            <div>
              <Button onClick={() => void navigate({ to: rolesTo(name) })}>
                Done
              </Button>
            </div>
          </div>
        </Panel>
      )}
      {!(creating && shown) && (
        <form
          className={cn("flex flex-col", gap)}
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <fieldset disabled={locked} className={cn("flex flex-col", gap)}>
            <Panel title="Role">
              <div className="grid grid-cols-1 gap-3 text-xs sm:grid-cols-2">
                <Field
                  label="Name"
                  hint={
                    managed
                      ? "The credentials' role keeps its name."
                      : creating
                        ? "Letters, digits and _ . $ -; not pg_…"
                        : "Renaming keeps its password (SCRAM)."
                  }
                >
                  <Input
                    value={f.name}
                    onChange={(e) => set("name", e.target.value)}
                    disabled={managed}
                    required
                    autoFocus={creating}
                    pattern="[A-Za-z_][A-Za-z0-9_.$\-]{0,62}"
                    className="font-mono"
                  />
                </Field>
                <Field label="Comment">
                  <Input
                    value={f.comment}
                    onChange={(e) => set("comment", e.target.value)}
                    placeholder="e.g. BI dashboards, read-only"
                  />
                </Field>
              </div>
            </Panel>
            <Panel title="Sign-in">
              <div className="flex flex-col gap-3 text-xs">
                {managed && (
                  <p className="text-muted">
                    The app role is the database's credentials: its password,
                    login and expiry are managed by SynCloud (see Connect on the
                    overview).
                  </p>
                )}
                <Toggle
                  checked={f.login}
                  onChange={(v) => set("login", v)}
                  disabled={managed}
                  label="Can sign in (LOGIN)"
                  hint="Turn off for a group role that only carries privileges for its members."
                />
                {f.login && !managed && (
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                    <Field label="Password">
                      <select
                        className={selectClass}
                        value={f.password}
                        onChange={(e) =>
                          set("password", e.target.value as PasswordMode)
                        }
                      >
                        {!creating && (
                          <option value="keep">
                            Keep the current one
                            {current?.hasPassword ? "" : " (none)"}
                          </option>
                        )}
                        <option value="generate">
                          Generate {creating ? "one" : "a new one"}
                        </option>
                        <option value="set">Type one</option>
                        <option value="none">
                          No password (cannot sign in)
                        </option>
                      </select>
                    </Field>
                    {f.password === "set" && (
                      <Field
                        label="New password"
                        hint="Sent hashed (SCRAM-SHA-256); never stored in SynCloud."
                      >
                        <Input
                          type="password"
                          value={f.passwordText}
                          onChange={(e) => set("passwordText", e.target.value)}
                          minLength={8}
                          required
                          autoComplete="new-password"
                        />
                      </Field>
                    )}
                    <Field label="Password expires" hint="Empty: never">
                      <Input
                        type="date"
                        value={f.validUntil}
                        onChange={(e) => set("validUntil", e.target.value)}
                      />
                    </Field>
                    <Field label="Connection limit" hint="Empty: no limit">
                      <Input
                        type="number"
                        min={-1}
                        value={f.connLimit}
                        onChange={(e) => set("connLimit", e.target.value)}
                      />
                    </Field>
                  </div>
                )}
              </div>
            </Panel>
            <Panel title="Abilities">
              <div className="grid grid-cols-1 gap-3 text-xs sm:grid-cols-3">
                <Toggle
                  checked={f.createDb}
                  onChange={(v) => set("createDb", v)}
                  label="Create databases"
                />
                <Toggle
                  checked={f.createRole}
                  onChange={(v) => set("createRole", v)}
                  label="Create and manage roles"
                  hint="Only roles it creates, never superusers."
                />
                <Toggle
                  checked={f.inherit}
                  onChange={(v) => set("inherit", v)}
                  label="Inherit privileges"
                  hint="Uses its groups' rights without SET ROLE."
                />
              </div>
              <p className="text-faint mt-2 text-xs">
                Superuser, replication and RLS bypass stay with the platform, as
                on hosted PostgreSQL.
              </p>
            </Panel>
            <Panel title="Membership">
              <div className="flex flex-col gap-1 text-xs">
                <p className="text-muted mb-1">
                  Roles this one belongs to. It gets their privileges (and with
                  admin, may grant them to others).
                </p>
                <div className="grid grid-cols-1 gap-1.5 sm:grid-cols-2 sm:gap-2 xl:grid-cols-3">
                  {members.map(([r, help]) => {
                    const m = isMember(r);
                    return (
                      <div key={r} className="flex min-w-0 flex-col gap-1">
                        <ChoiceCard
                          multi
                          selected={!!m}
                          onSelect={() => toggleMember(r)}
                          title={<span className="font-mono">{r}</span>}
                          description={help}
                          className="flex-1"
                        />
                        {m && (
                          <Toggle
                            checked={m.admin}
                            onChange={() =>
                              set(
                                "memberOf",
                                f.memberOf.map((x) =>
                                  x.role === r ? { ...x, admin: !x.admin } : x,
                                ),
                              )
                            }
                            label="admin: may grant it to others"
                          />
                        )}
                      </div>
                    );
                  })}
                </div>
              </div>
            </Panel>
            {creating && (
              <Panel title="Access">
                <div className="flex flex-col gap-2 text-xs">
                  <Field
                    label={`On ${name.replace(/-/g, "_")}`}
                    hint="Every schema, every table and sequence, now and later. Fine-grained grants are on each object's Privileges tab."
                  >
                    <select
                      className={cn(inlineSelect, "max-w-60")}
                      value={f.access}
                      onChange={(e) =>
                        set("access", e.target.value as RoleForm["access"])
                      }
                    >
                      <option value="">None yet</option>
                      <option value="read">Read only</option>
                      <option value="write">Read and write</option>
                    </select>
                  </Field>
                </div>
              </Panel>
            )}
          </fieldset>
          {save.error && <Alert>{errText(save.error)}</Alert>}
          {!locked && (
            <div className="flex items-center gap-2">
              <Button type="submit" variant="primary" disabled={save.isPending}>
                {creating ? "Create role" : "Save"}
              </Button>
              <Link to={rolesTo(name)}>
                <Button type="button" variant="ghost">
                  Cancel
                </Button>
              </Link>
            </div>
          )}
        </form>
      )}
      {!creating && current && !locked && (
        <DatabaseAccess
          path={path}
          role={current.name}
          dbs={(dbs.data ?? []).filter((d) => !d.protected).map((d) => d.name)}
        />
      )}
      {!creating && current && !locked && !managed && (
        <DropRole
          path={path}
          name={name}
          role={current}
          roles={roles.data ?? []}
        />
      )}
    </div>
  );
}

function Secret({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center gap-2">
      <span className="text-muted w-16 shrink-0">{label}</span>
      <code className="bg-bg border-line min-w-0 flex-1 truncate rounded-sm border px-2 py-1 font-mono">
        {value}
      </code>
      <IconButton
        label={`Copy ${label}`}
        onClick={() => void navigator.clipboard.writeText(value)}
      >
        <Copy className="size-3.5" />
      </IconButton>
    </div>
  );
}

/** Read, write or no access to each database, in one step. */
function DatabaseAccess({
  path,
  role,
  dbs,
}: {
  path: string;
  role: string;
  dbs: string[];
}) {
  const qc = useQueryClient();
  const [ran, setRan] = useState<Record<string, string>>({});
  const apply = useMutation({
    mutationFn: ({ db, access }: { db: string; access: string }) =>
      api<{ statements: string[] }>(
        "POST",
        `${path}/pg/privileges?db=${encodeURIComponent(db)}`,
        { preset: { role, access } },
      ),
    onSuccess: (r, v) => {
      setRan((x) => ({
        ...x,
        [v.db]: `${v.access === "none" ? "Access removed" : v.access === "read" ? "Read-only access" : "Read-write access"} (${r.statements.length} statements)`,
      }));
      void qc.invalidateQueries({ queryKey: ["pg", path, "privileges"] });
    },
  });
  return (
    <Panel title="Access to databases">
      <div className="flex flex-col gap-2 text-xs">
        <p className="text-muted">
          Sets the role's rights on every schema, table and sequence of a
          database, including ones created later by their owners. Fine-grained
          grants are on each object's Privileges tab in the Explorer.
        </p>
        {dbs.map((db) => (
          <div key={db} className="flex flex-wrap items-center gap-2">
            <span className="w-40 truncate font-mono">{db}</span>
            {(["read", "write", "none"] as const).map((a) => (
              <Button
                key={a}
                disabled={apply.isPending}
                onClick={() => apply.mutate({ db, access: a })}
              >
                {a === "read"
                  ? "Read only"
                  : a === "write"
                    ? "Read and write"
                    : "No access"}
              </Button>
            ))}
            {ran[db] && (
              <span className="text-ok flex items-center gap-1">
                <ShieldCheck className="size-3.5" />
                {ran[db]}
              </span>
            )}
          </div>
        ))}
        {apply.error && <Alert>{errText(apply.error)}</Alert>}
      </div>
    </Panel>
  );
}

function DropRole({
  path,
  name,
  role,
  roles,
}: {
  path: string;
  name: string;
  role: PgRole;
  roles: PgRole[];
}) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [to, setTo] = useState("app");
  const [typed, setTyped] = useState("");
  const drop = useMutation({
    mutationFn: () =>
      api(
        "DELETE",
        `${path}/pg/roles/${encodeURIComponent(role.name)}?reassignTo=${encodeURIComponent(to)}`,
      ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["pg", path] });
      void navigate({ to: rolesTo(name) });
    },
  });
  return (
    <Panel title="Drop role">
      <div className="flex flex-col gap-2 text-xs">
        <p className="text-muted">
          Its tables, views and other objects in every database go to the role
          below; its privileges are revoked; then the role is dropped.
          {role.connections > 0 && (
            <span className="text-warn">
              {" "}
              It has {role.connections} open session(s), which keep working
              until they disconnect.
            </span>
          )}
        </p>
        <div className="flex flex-wrap items-end gap-2">
          <Field label="Objects go to">
            <select
              className={cn(inlineSelect, "w-48")}
              value={to}
              onChange={(e) => setTo(e.target.value)}
            >
              {roles
                .filter((r) => !r.protected && r.name !== role.name)
                .map((r) => (
                  <option key={r.name}>{r.name}</option>
                ))}
            </select>
          </Field>
          <Field label={`Type ${role.name} to confirm`}>
            <Input
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              className="w-48 font-mono"
            />
          </Field>
          <Button
            variant="danger"
            disabled={typed !== role.name || drop.isPending}
            onClick={() => drop.mutate()}
          >
            Drop role
          </Button>
        </div>
        {drop.error && <Alert>{errText(drop.error)}</Alert>}
      </div>
    </Panel>
  );
}
